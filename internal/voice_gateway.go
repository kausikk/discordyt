package internal

import (
	"context"
	"encoding/json"
	"log"
	"time"

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

type voiceGatewayState int8

const (
	vGwClosed voiceGatewayState = iota
	vGwReady
	vGwResuming
)

type voiceGateway struct {
	state         voiceGatewayState
	botAppId      string
	guildId       string
	sessionId     string
	token         string
	endpoint      string
	ws            *websocket.Conn
	heartbeatIntv int64
	ssrc          uint32
	ip            string
	port          int64
	secretKey     [32]byte
	sequence      uint16
	timestamp     uint32
}

func voiceConnect(rootctx context.Context, botAppId, guildId, sessionId, token, endpoint string) (*voiceGateway, error) {
	var err error
	payload := voiceGwPayload{}

	// Init gateway
	voiceGw := voiceGateway{
		botAppId:  botAppId,
		guildId:   guildId,
		sessionId: sessionId,
		token:     token,
		endpoint:  endpoint,
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

func (voiceGw *voiceGateway) Listen(rootctx context.Context) error {
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
			case voiceHeartbeat:
				// Send heartbeat
				err = vSend(voiceGw.ws, gwCtx, &cachedHeartbeat)
				if err != nil {
					keepReading = false
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
		payload.Op = voiceResume
		payload.D, _ = json.Marshal(&resumeData)
		if err = vSend(voiceGw.ws, rootctx, &payload); err != nil {
			return err
		}

		// Change to READY state
		voiceGw.state = vGwReady
	}
}

func (voiceGw *voiceGateway) Speaking(rootctx context.Context, isSpeak bool) error {
	val := int64(1)
	if !isSpeak {
		val = 0
	}
	data, _ := json.Marshal(voiceSpeakingData{
		Speaking: val,
		Delay:    0,
		Ssrc:     voiceGw.ssrc,
	})
	payload := voiceGwPayload{
		Op: voiceSpeaking,
		D:  data,
	}
	return vSend(voiceGw.ws, rootctx, &payload)
}

func (voiceGw *voiceGateway) Close(rootctx context.Context) error {
	voiceGw.state = vGwClosed
	voiceGw.ws.Close(websocket.StatusNormalClosure, "")
	return nil
}

func voiceGwHeartbeat(voiceGw *voiceGateway, ctx context.Context) error {
	for {
		if err := vSend(voiceGw.ws, ctx, &cachedHeartbeat); err != nil {
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
