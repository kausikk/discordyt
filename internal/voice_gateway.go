package internal

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"nhooyr.io/websocket"
)

var CachedSelectPrtcl = voiceGwPayload{
	Op: VoiceSelectPrtcl,
	D: CachedSelectPrtclData,
}
var CachedSelectPrtclData, _ = json.Marshal(voiceSelectPrtclData{
	Protocol: "udp",
	Data: voiceSelectPrtclSubData{
		Addr: "127.0.0.1",
		Port: 8080,
		Mode: "xsalsa20_poly1305_lite",
	},
})

var CachedHeartbeat = voiceGwPayload{
	Op: VoiceHeartbeat,
	// Voice nonce = 123403290
	D: []byte{49, 50, 51, 52, 48, 51, 50, 57, 48},
}

type VoiceGatewayState int8

const (
	VGwClosed VoiceGatewayState = iota
	VGwReady
	VGwResuming
)

type VoiceGateway struct {
	State         VoiceGatewayState
	botAppId      string
	guildId       string
	sessionId     string
	token         string
	endpoint      string
	ws            *websocket.Conn
	heartbeatIntv int64
	ssrc          int32
	ip            string
	port          int64
	secretKey     []byte
}

func VoiceConnect(rootctx context.Context, botAppId, guildId, sessionId, token, endpoint string) (*VoiceGateway, error) {
	var err error
	payload := voiceGwPayload{}

	// Init gateway
	voiceGw := VoiceGateway{
		botAppId:  botAppId,
		guildId:   guildId,
		sessionId: sessionId,
		token:     token,
		endpoint:  endpoint,
	}

	// Connect to Discord websocket
	dialCtx, dialCancel := context.WithTimeout(rootctx, DefaultTimeout)
	voiceGw.ws, _, err = websocket.Dial(dialCtx, voiceGw.endpoint, nil)
	dialCancel()
	if err != nil {
		return nil, err
	}
	defer func() {
		if voiceGw.State == VGwClosed {
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
	payload.Op = VoiceIdentify
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
	if err = vSend(voiceGw.ws, rootctx, &CachedSelectPrtcl); err != nil {
		return nil, err
	}

	// Receive description
	if err = vRead(voiceGw.ws, rootctx, &payload); err != nil {
		return nil, err
	}
	sessData := voiceSessDesc{}
	json.Unmarshal(payload.D, &sessData)

	// Store secret key
	voiceGw.secretKey = sessData.SecretKey

	// Change to READY state
	voiceGw.State = VGwReady
	return &voiceGw, nil
}

func (voiceGw *VoiceGateway) Listen(rootctx context.Context) error {
	var err error
	payload := voiceGwPayload{}

	// If resume loop ends, close gateway
	defer func() {
		voiceGw.State = VGwClosed
		voiceGw.ws.Close(websocket.StatusNormalClosure, "")
	}()

	// Enter resume loop
	for {
		// Start heartbeat
		gwCtx, gwCancel := context.WithCancel(rootctx)
		go voiceHeartbeat(voiceGw, gwCtx)

		// Enter read loop
		keepReading := true
		for keepReading {
			if err = vRead(voiceGw.ws, gwCtx, &payload); err != nil {
				log.Println("voice gw read err:", err)
				keepReading = false
				break
			}

			// Handle event according to opcode
			switch payload.Op {
			case VoiceHeartbeat:
				// Send heartbeat
				err = vSend(voiceGw.ws, gwCtx, &CachedHeartbeat)
				if err != nil {
					keepReading = false
				}
			case VoiceResumed:
				// Do nothing
			case VoiceSessDesc:
				// Do nothing
			}
		}

		// Change to Resuming state
		// Cancel all child tasks
		voiceGw.State = VGwResuming
		gwCancel()

		// If root ctx cancelled, dont attempt resume
		if rootctx.Err() != nil {
			return err
		}

		// Check if gateway can be resumed
		status := websocket.CloseStatus(err)
		log.Printf("voice close code: %d\n", status)
		canResume, exists := VoiceValidResumeCodes[status]
		if !canResume && exists {
			log.Println("voice can't resume")
			return err
		}

		// Close websocket
		voiceGw.ws.Close(websocket.StatusServiceRestart, "")

		// Connect to resume url
		dialCtx, dialCancel := context.WithTimeout(
			rootctx, DefaultTimeout)
		voiceGw.ws, _, err = websocket.Dial(dialCtx, voiceGw.endpoint, nil)
		dialCancel()
		if err != nil {
			return err
		}

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
		payload.Op = VoiceResume
		payload.D, _ = json.Marshal(&resumeData)
		if err = vSend(voiceGw.ws, rootctx, &payload); err != nil {
			return err
		}

		// Change to READY state
		voiceGw.State = VGwReady
	}
}

func (voiceGw *VoiceGateway) Close(rootctx context.Context) error {
	if voiceGw.State != VGwClosed {
		voiceGw.State = VGwClosed
		voiceGw.ws.Close(websocket.StatusNormalClosure, "")
	}
	return nil
}

func voiceHeartbeat(voiceGw *VoiceGateway, ctx context.Context) error {
	for {
		if err := vSend(voiceGw.ws, ctx, &CachedHeartbeat); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(
			time.Duration(voiceGw.heartbeatIntv) * time.Millisecond):
		}
	}
}

func vRead(c *websocket.Conn, ctx context.Context, payload *voiceGwPayload) error {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	_, raw, err := c.Read(ctx)
	if err != nil {
		return err
	}
	json.Unmarshal(raw, payload) // Unhandled err
	if payload.Op != VoiceHeartbeatAck {
		log.Println("voice read: op:", VoiceOpcodeNames[payload.Op])
	}
	return err
}

func vSend(c *websocket.Conn, ctx context.Context, payload *voiceGwPayload) error {
	encoded, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	err := c.Write(ctx, websocket.MessageText, encoded)
	if payload.Op != VoiceHeartbeat {
		log.Println("voice send: op:", VoiceOpcodeNames[payload.Op])
	}
	return err
}
