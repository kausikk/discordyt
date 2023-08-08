package internal

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"nhooyr.io/websocket"
)

type VoiceGatewayState int8

const (
	VGwClosed VoiceGatewayState = iota
	VGwReady
	VGwResuming
)

type VoiceGateway struct {
	State         VoiceGatewayState
	ws            *websocket.Conn
	heartbeatIntv int64
	botAppId      string
	guildId       string
	sessionId     string
	token         string
	endpoint      string
	ssrc          int64
	ip            string
	port          int64
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

	// TODO
	// Select protocol
	// Receive description

	// Change to READY state
	voiceGw.State = VGwReady
	return &voiceGw, nil
}

func (voiceGw *VoiceGateway) Listen(rootctx context.Context) error {
	return voiceHeartbeat(voiceGw, rootctx)
}

func (voiceGw *VoiceGateway) Close(rootctx context.Context) error {
	if voiceGw.State != VGwClosed {
		voiceGw.ws.Close(websocket.StatusNormalClosure, "")
		voiceGw.State = VGwClosed
	}
	return nil
}

func voiceHeartbeat(voiceGw *VoiceGateway, ctx context.Context) error {
	heartbeat := voiceGwPayload{Op: VoiceHeartbeat}
	heartbeat.D, _ = json.Marshal(VoiceNonce)
	for {
		if err := vSend(voiceGw.ws, ctx, &heartbeat); err != nil {
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
	log.Printf("voice read: op: %d data: %s", payload.Op, string(payload.D))
	return err
}

func vSend(c *websocket.Conn, ctx context.Context, payload *voiceGwPayload) error {
	encoded, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	err := c.Write(ctx, websocket.MessageText, encoded)
	log.Printf("voice sent: op: %d data: %s", payload.Op, string(payload.D))
	return err
}
