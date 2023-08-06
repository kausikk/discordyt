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

func Run(rootctx context.Context, config map[string]string) error {
	// Connect to Discord websocket
	dialCtx, dialCancel := context.WithTimeout(rootctx, DefaultTimeout)
	c, _, err := websocket.Dial(dialCtx, DiscordWSS, nil)
	dialCancel()
	if err != nil {
		return err
	}
	defer c.Close(websocket.StatusInternalError, "")

	readPayload := gatewayRead{}
	sendPayload := gatewaySend{}

	// Receive HELLO event
	if err = read(c, rootctx, &readPayload); err != nil {
		return err
	}
	helloData := helloData{}
	json.Unmarshal(readPayload.D, &helloData)

	// Send IDENTIFY event
	idData := identifyData{
		Token:      config["BOT_TOKEN"],
		Intents:    GATEWAY_INTENTS,
		Properties: GATEWAY_PROPERTIES,
	}
	sendPayload.Op = OPC_IDENTIFY
	sendPayload.D, _ = json.Marshal(&idData)
	if err = send(c, rootctx, &sendPayload); err != nil {
		return err
	}

	// Receive READY or INVALID_SESSION event
	if err = read(c, rootctx, &readPayload); err != nil {
		return err
	}
	if readPayload.Op == OPC_INVALID_SESSION {
		return errors.New("received INVALID_SESSION after IDENTIFY")
	}
	readyData := readyData{}
	json.Unmarshal(readPayload.D, &readyData)

	// Create var for sequence number
	lastSeq := readPayload.S

	// Enter resume loop
	for {
		gwCtx, gwCancel := context.WithCancel(rootctx)
		go heartbeat(c, gwCtx, helloData.Interval, &lastSeq)

		// Enter read loop
		keepReading := true
		for keepReading {
			if err = read(c, gwCtx, &readPayload); err != nil {
				log.Println("GW read err: ", err)
				keepReading = false
				break
			}

			// Store sequence number and handle event
			lastSeq = readPayload.S
			switch readPayload.Op {
			case OPC_HEARTBEAT:
				// Send heartbeat
				sendPayload.Op = OPC_HEARTBEAT
				sendPayload.D, _ = json.Marshal(lastSeq)
				if err = send(c, gwCtx, &sendPayload); err != nil {
					keepReading = false
				}
			case OPC_RECONNECT:
				// Close with ServiceRestart to trigger resume
				// Errors on next read or send
				c.Close(websocket.StatusServiceRestart, "")
			case OPC_INVALID_SESSION:
				// Close with InvalidSession to avoid resume
				// Errors on next read or send
				c.Close(StatusGatewayInvalidSession, "")
			case OPC_DISPATCH:
				// Handle dispatch
				if err = handleDispatch(c, gwCtx, &readPayload); err != nil {
					keepReading = false
				}
			}
		}
		// Cancel all child tasks
		gwCancel()

		// If root ctx cancelled, dont attempt resume
		if rootctx.Err() != nil {
			c.Close(websocket.StatusNormalClosure, "")
			return err
		}

		// Check if gateway can be resumed
		status := websocket.CloseStatus(err)
		log.Printf("close code: %d\n", status)
		canResume, exists := ValidResumeCodes[status]
		if !canResume && exists {
			log.Println("unable to resume")
			c.Close(websocket.StatusInternalError, "")
			return err
		}

		// Close, then connect to resume url
		c.Close(websocket.StatusServiceRestart, "")
		dialCtx, dialCancel = context.WithTimeout(rootctx, DefaultTimeout)
		c, _, err = websocket.Dial(dialCtx, readyData.ResumeUrl, nil)
		dialCancel()
		if err != nil {
			c.Close(websocket.StatusInternalError, "")
			return err
		}

		// Receive HELLO event
		if err = read(c, rootctx, &readPayload); err != nil {
			c.Close(websocket.StatusInternalError, "")
			return err
		}
		json.Unmarshal(readPayload.D, &helloData)

		// Send RESUME event
		resumeData := resumeData{
			Token:     config["BOT_TOKEN"],
			SessionId: readyData.SessionId,
			S:         lastSeq,
		}
		sendPayload.Op = OPC_RESUME
		sendPayload.D, _ = json.Marshal(&resumeData)
		if err = send(c, rootctx, &sendPayload); err != nil {
			c.Close(websocket.StatusInternalError, "")
			return err
		}
	}
}

func handleDispatch(c *websocket.Conn, ctx context.Context,
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

func heartbeat(c *websocket.Conn, ctx context.Context, intv int64,
	lastSeq *int64) error {
	heartbeat := gatewaySend{OPC_HEARTBEAT, nil}
	for {
		heartbeat.D, _ = json.Marshal(*lastSeq)
		if err := send(c, ctx, &heartbeat); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(intv) * time.Millisecond):
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
