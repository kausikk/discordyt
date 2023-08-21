package internal

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"

	"golang.org/x/crypto/nacl/secretbox"
	"nhooyr.io/websocket"
)

var cachedSelectPrtcl = voiceGwPayload{
	Op: voiceSelectPrtcl,
	D:  cachedSelectPrtclData,
}
var cachedSelectPrtclData, _ = json.Marshal(voiceSelectPrtclData{
	Protocol: "udp",
	Data: voiceSelectPrtclSubData{
		Addr: "127.0.0.1",
		Port: 8080,
		Mode: "xsalsa20_poly1305",
	},
})

var cachedHeartbeat = voiceGwPayload{
	Op: voiceHeartbeat,
	// Voice nonce = 123403290
	D: []byte{49, 50, 51, 52, 48, 51, 50, 57, 48},
}

type vGwState int8

const (
	vGwClosed vGwState = iota
	vGwReady
	vGwResuming
)

type voiceGateway struct {
	state         vGwState
	botAppId      string
	guildId       string
	sessionId     string
	token         string
	endpoint      string
	packets       <-chan []byte
	ws            *websocket.Conn
	heartbeatIntv int64
	ssrc          uint32
	ip            string
	port          int64
	secretKey     [32]byte
}

func voiceConnect(rootctx context.Context, packets <-chan []byte, botAppId, guildId, sessionId, token, endpoint string) (*voiceGateway, error) {
	var err error
	payload := voiceGwPayload{}

	// Init gateway
	voiceGw := voiceGateway{
		botAppId:  botAppId,
		guildId:   guildId,
		sessionId: sessionId,
		token:     token,
		endpoint:  endpoint,
		packets:   packets,
	}

	// Connect to Discord websocket
	dialCtx, dialCancel := context.WithTimeout(rootctx, defaultTimeout)
	voiceGw.ws, _, err = websocket.Dial(dialCtx, voiceGw.endpoint, nil)
	dialCancel()
	if err != nil {
		return nil, err
	}
	defer func() {
		if voiceGw.state == vGwClosed {
			voiceGw.ws.Close(websocket.StatusInternalError, "")
		}
	}()

	// Receive HELLO event
	if err = vRead(voiceGw.ws, rootctx, &payload); err != nil {
		return nil, err
	}
	helloData := voiceHelloData{}
	json.Unmarshal(payload.D, &helloData)

	// Store hb interval
	voiceGw.heartbeatIntv = int64(helloData.Interval)

	// Send IDENTIFY event
	idData := voiceIdentifyData{
		ServerId:  voiceGw.guildId,
		UserId:    voiceGw.botAppId,
		SessionId: voiceGw.sessionId,
		Token:     voiceGw.token,
	}
	payload.Op = voiceIdentify
	payload.D, _ = json.Marshal(&idData)
	if err = vSend(voiceGw.ws, rootctx, &payload); err != nil {
		return nil, err
	}

	// Receive READY event
	if err = vRead(voiceGw.ws, rootctx, &payload); err != nil {
		return nil, err
	}
	readyData := voiceReadyData{}
	json.Unmarshal(payload.D, &readyData)

	// Store voice UDP data and ssrc
	voiceGw.ssrc = readyData.Ssrc
	voiceGw.ip = readyData.Ip
	voiceGw.port = readyData.Port

	// Send SELECT PROTOCOL event
	if err = vSend(voiceGw.ws, rootctx, &cachedSelectPrtcl); err != nil {
		return nil, err
	}

	// Receive description
	// Sometimes opcodes 18, 20 (unknown), or 5 (speaking) are sent
	for payload.Op != voiceSessDesc {
		if err = vRead(voiceGw.ws, rootctx, &payload); err != nil {
			return nil, err
		}
	}
	sessData := voiceSessionDesc{}
	json.Unmarshal(payload.D, &sessData)

	// Store secret key
	json.Unmarshal(sessData.SecretKey, &voiceGw.secretKey)

	// Change to READY state
	voiceGw.state = vGwReady
	return &voiceGw, nil
}

func (voiceGw *voiceGateway) Serve(rootctx context.Context) error {
	var err error
	payload := voiceGwPayload{}

	// If resume loop ends, close gateway
	defer func() {
		voiceGw.state = vGwClosed
		voiceGw.ws.Close(websocket.StatusNormalClosure, "")
	}()

	// Enter resume loop
	for {
		// Start heartbeat
		gwCtx, gwCancel := context.WithCancel(rootctx)
		go voiceGwHeartbeat(voiceGw, gwCtx)
		go voiceGwUdp(voiceGw, gwCtx)

		// Enter read loop
		isReading := true
		for isReading {
			if err = vRead(voiceGw.ws, gwCtx, &payload); err != nil {
				log.Println("voice gw read err:", err)
				isReading = false
				break
			}

			// Handle event according to opcode
			switch payload.Op {
			case voiceHeartbeat:
				// Send heartbeat
				err = vSend(voiceGw.ws, gwCtx, &cachedHeartbeat)
				if err != nil {
					isReading = false
				}
			case voiceResumed:
				// Do nothing
			case voiceSessDesc:
				// Do nothing
			}
		}

		// Change to Resuming state
		// Cancel all child tasks
		voiceGw.state = vGwResuming
		gwCancel()

		// If root ctx cancelled, dont attempt resume
		if rootctx.Err() != nil {
			return err
		}

		// Check if gateway can be resumed
		status := websocket.CloseStatus(err)
		log.Printf("voice close code: %d\n", status)
		canResume, exists := voiceValidResumeCodes[status]
		if !canResume && exists {
			log.Println("voice can't resume")
			return err
		}

		// Close websocket
		voiceGw.ws.Close(websocket.StatusServiceRestart, "")

		// Connect to resume url
		dialCtx, dialCancel := context.WithTimeout(
			rootctx, defaultTimeout)
		newWs, _, err := websocket.Dial(dialCtx, voiceGw.endpoint, nil)
		dialCancel()
		if err != nil {
			return err
		}
		voiceGw.ws = newWs

		// Receive HELLO event
		if err = vRead(voiceGw.ws, rootctx, &payload); err != nil {
			return err
		}
		helloData := voiceHelloData{}
		json.Unmarshal(payload.D, &helloData)

		// Store hb interval
		voiceGw.heartbeatIntv = int64(helloData.Interval)

		// Send RESUME event
		resumeData := voiceResumeData{
			ServerId:  voiceGw.guildId,
			SessionId: voiceGw.sessionId,
			Token:     voiceGw.token,
		}
		payload.Op = voiceResume
		payload.D, _ = json.Marshal(&resumeData)
		if err = vSend(voiceGw.ws, rootctx, &payload); err != nil {
			return err
		}

		// Change to READY state
		voiceGw.state = vGwReady
	}
}

func (voiceGw *voiceGateway) Close() error {
	voiceGw.state = vGwClosed
	voiceGw.ws.Close(websocket.StatusNormalClosure, "")
	return nil
}

func voiceGwHeartbeat(voiceGw *voiceGateway, ctx context.Context) error {
	interval := time.Duration(voiceGw.heartbeatIntv) * time.Millisecond
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {	
		if err := vSend(voiceGw.ws, ctx, &cachedHeartbeat); err != nil {
			return err
		}
		select {
		case <-timer.C:
			timer.Reset(interval)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func voiceGwUdp(voiceGw *voiceGateway, ctx context.Context) {
	// Open voice udp socket
	url := fmt.Sprintf(
		"%s:%d", voiceGw.ip, voiceGw.port,
	)
	sock, err := net.Dial("udp", url)
	if err != nil {
		return
	}
	defer sock.Close()

	// Send speaking payload
	data, _ := json.Marshal(voiceSpeakingData{
		Speaking: 1,
		Delay:    0,
		Ssrc:     voiceGw.ssrc,
	})
	payload := voiceGwPayload{
		Op: voiceSpeaking,
		D:  data,
	}
	err = vSend(voiceGw.ws, ctx, &payload)
	if err != nil {
		return
	}

	// xsalsa20_poly1305 stuff, see
	// https://github.com/bwmarrin/discordgo
	// https://discord.com/developers/docs/topics/
	// voice-connections#encrypting-and-sending-voice
	nonce := [nonceLen]byte{}
	header := make([]byte, rtpHeaderLen)
	header[0] = 0x80
	header[1] = 0x78
	binary.BigEndian.PutUint32(header[8:], voiceGw.ssrc)

	var sequence uint16
	var timestamp uint32

	for {
		select {
		case packet := <-voiceGw.packets:
			// more xsalsa20_poly1305 stuff
			binary.BigEndian.PutUint16(header[2:], sequence)
			binary.BigEndian.PutUint32(header[4:], timestamp)
			sequence += 1
			timestamp += 960
			copy(nonce[:], header)
			encrypted := secretbox.Seal(
				header,
				packet,
				&nonce,
				&voiceGw.secretKey,
			)
			_, err := sock.Write(encrypted)
			if err != nil {
				log.Println("udp err:", err)
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func vRead(c *websocket.Conn, ctx context.Context, payload *voiceGwPayload) error {
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	_, raw, err := c.Read(ctx)
	if err != nil {
		return err
	}
	json.Unmarshal(raw, payload) // Unhandled err
	if payload.Op != voiceHeartbeatAck {
		log.Println("voice read: op:", voiceOpcodeNames[payload.Op])
	}
	return err
}

func vSend(c *websocket.Conn, ctx context.Context, payload *voiceGwPayload) error {
	encoded, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	err := c.Write(ctx, websocket.MessageText, encoded)
	if payload.Op != voiceHeartbeat {
		log.Println("voice send: op:", voiceOpcodeNames[payload.Op])
	}
	return err
}
