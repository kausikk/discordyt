package internal

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"nhooyr.io/websocket"
)

const DiscordWSS = "wss://gateway.discord.gg"
const DefaultTimeout = 5 * time.Minute

type GatewayState int8

const (
	GwClosed GatewayState = iota
	GwReady
	GwResuming
)

type Gateway struct {
	State         GatewayState
	Ws            *websocket.Conn
	LastSeq       int64
	ResumeUrl     string
	SessionId     string
	HeartbeatIntv int64
	BotToken      string
}

func Connect(rootctx context.Context, config map[string]string) (Gateway, error) {
	var err error
	readPayload := gatewayRead{}
	sendPayload := gatewaySend{}

	// Init gateway
	gw := Gateway{}
	gw.BotToken = config["BOT_TOKEN"]

	// Connect to Discord websocket
	dialCtx, dialCancel := context.WithTimeout(rootctx, DefaultTimeout)
	gw.Ws, _, err = websocket.Dial(dialCtx, DiscordWSS, nil)
	dialCancel()
	if err != nil {
		return gw, err
	}
	defer func() {
		if gw.State == GwClosed {
			gw.Ws.Close(websocket.StatusInternalError, "")
		}
	}()

	// Receive HELLO event
	if err = read(gw.Ws, rootctx, &readPayload); err != nil {
		return gw, err
	}
	helloData := helloData{}
	json.Unmarshal(readPayload.D, &helloData)

	// Store hb interval
	gw.HeartbeatIntv = helloData.Interval

	// Send IDENTIFY event
	idData := identifyData{
		Token:      gw.BotToken,
		Intents:    GATEWAY_INTENTS,
		Properties: GATEWAY_PROPERTIES,
	}
	sendPayload.Op = OPC_IDENTIFY
	sendPayload.D, _ = json.Marshal(&idData)
	if err = send(gw.Ws, rootctx, &sendPayload); err != nil {
		return gw, err
	}

	// Receive READY or INVALID_SESSION event
	if err = read(gw.Ws, rootctx, &readPayload); err != nil {
		return gw, err
	}
	if readPayload.Op == OPC_INVALID_SESSION {
		return gw,
			errors.New("received INVALID_SESSION after IDENTIFY")
	}
	readyData := readyData{}
	json.Unmarshal(readPayload.D, &readyData)

	// Store session resume data
	gw.SessionId = readyData.SessionId
	gw.ResumeUrl = readyData.ResumeUrl
	gw.LastSeq = readPayload.S

	// Change to READY state
	gw.State = GwReady
	return gw, nil
}

func (gw *Gateway) Listen(rootctx context.Context) error {
	var err error
	readPayload := gatewayRead{}
	sendPayload := gatewaySend{}

	// If resume loop ends, close gateway
	defer func() {
		gw.State = GwClosed
		gw.Ws.Close(websocket.StatusNormalClosure, "")
	}()

	// Enter resume loop
	for {
		// Start heartbeat
		gwCtx, gwCancel := context.WithCancel(rootctx)
		go heartbeat(gw, gwCtx)

		// Enter read loop
		keepReading := true
		for keepReading {
			if err = read(gw.Ws, gwCtx, &readPayload); err != nil {
				log.Println("GW read err: ", err)
				keepReading = false
				break
			}

			// Store sequence number
			gw.LastSeq = readPayload.S

			// Handle event according to opcode
			switch readPayload.Op {
			case OPC_HEARTBEAT:
				// Send heartbeat
				sendPayload.Op = OPC_HEARTBEAT
				sendPayload.D, _ = json.Marshal(gw.LastSeq)
				err = send(gw.Ws, gwCtx, &sendPayload)
				if err != nil {
					keepReading = false
				}
			case OPC_RECONNECT:
				// Close with ServiceRestart to trigger resume
				// Errors on next read or send
				gw.Ws.Close(websocket.StatusServiceRestart, "")
			case OPC_INVALID_SESSION:
				// Close with InvalidSession to avoid resume
				// Errors on next read or send
				gw.Ws.Close(StatusGatewayInvalidSession, "")
			case OPC_DISPATCH:
				// Handle dispatch
				err = handleDispatch(gw, gwCtx, &readPayload)
				if err != nil {
					keepReading = false
				}
			}
		}

		// Change to Resuming state
		// Cancel all child tasks
		gw.State = GwResuming
		gwCancel()

		// If root ctx cancelled, dont attempt resume
		if rootctx.Err() != nil {
			return err
		}

		// Check if gateway can be resumed
		status := websocket.CloseStatus(err)
		log.Printf("close code: %d\n", status)
		canResume, exists := ValidResumeCodes[status]
		if !canResume && exists {
			log.Println("can't resume")
			return err
		}

		// Close websocket
		gw.Ws.Close(websocket.StatusServiceRestart, "")

		// Connect to resume url
		dialCtx, dialCancel := context.WithTimeout(
			rootctx, DefaultTimeout)
		gw.Ws, _, err = websocket.Dial(dialCtx, gw.ResumeUrl, nil)
		dialCancel()
		if err != nil {
			return err
		}

		// Receive HELLO event
		if err = read(gw.Ws, rootctx, &readPayload); err != nil {
			return err
		}
		helloData := helloData{}
		json.Unmarshal(readPayload.D, &helloData)

		// Store hb interval
		gw.HeartbeatIntv = helloData.Interval

		// Send RESUME event
		resumeData := resumeData{
			Token:     gw.BotToken,
			SessionId: gw.SessionId,
			S:         gw.LastSeq,
		}
		sendPayload.Op = OPC_RESUME
		sendPayload.D, _ = json.Marshal(&resumeData)
		if err = send(gw.Ws, rootctx, &sendPayload); err != nil {
			return err
		}

		// Change to READY state
		gw.State = GwReady
	}
}

func handleDispatch(gw *Gateway, ctx context.Context,
	payload *gatewayRead) error {
	switch payload.T {
	case "VOICE_STATE_UPDATE":
		voiceData := voiceStateData{}
		json.Unmarshal(payload.D, &voiceData)
		log.Printf("guild: %s channel: %s user: %s\n",
			voiceData.GuildId,
			voiceData.ChannelId,
			voiceData.UserId)
	case "RESUMED":
		// Do nothing
	case "VOICE_CHANNEL_STATUS_UPDATE":
		// Do nothing
	default:
		log.Println("unhandled dispatch:", payload.T)
	}
	return nil
}

func heartbeat(gw *Gateway, ctx context.Context) error {
	heartbeat := gatewaySend{OPC_HEARTBEAT, nil}
	for {
		heartbeat.D, _ = json.Marshal(gw.LastSeq)
		if err := send(gw.Ws, ctx, &heartbeat); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(
			time.Duration(gw.HeartbeatIntv) * time.Millisecond):
		}
	}
}

func read(c *websocket.Conn, ctx context.Context,
	payload *gatewayRead) error {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	_, raw, err := c.Read(ctx)
	if err != nil {
		return err
	}
	// Reset T and D fields since these are optional
	payload.T = ""
	payload.D = nil
	json.Unmarshal(raw, payload) // Unhandled err
	log.Printf("read: op:%d s:%d t:%s\n",
		payload.Op, payload.S, payload.T)
	return err
}

func send(c *websocket.Conn, ctx context.Context,
	payload *gatewaySend) error {
	encoded, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	err := c.Write(ctx, websocket.MessageText, encoded)
	log.Printf("sent: op:%d\n", payload.Op)
	return err
}
