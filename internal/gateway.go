package internal

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/orcaman/concurrent-map/v2"
	"nhooyr.io/websocket"
)

const DiscordWSS = "wss://gateway.discord.gg"
const DefaultTimeout = 2 * time.Minute

type GatewayState int8

const (
	GwClosed GatewayState = iota
	GwReady
	GwResuming
)

type Gateway struct {
	State           GatewayState
	botToken        string
	botAppId        string
	botPublicKey    string
	userOccupancy   cmap.ConcurrentMap[string, string]
	guildStates     cmap.ConcurrentMap[string, *GuildState]
	guildStatesLock sync.Mutex
	ws              *websocket.Conn
	lastSeq         int64
	resumeUrl       string
	sessionId       string
	heartbeatIntv   int64
}

type GuildState struct {
	guildId       string
	botChnlId     *string
	voiceSessId   string
	voiceToken    string
	voiceEndpoint string
	freshTokEnd   bool
	freshChnlSess bool
	voiceGw       *VoiceGateway
	joinLock      sync.Mutex
	joinedChnl    chan *string
}

func Connect(rootctx context.Context, botToken, botAppId, botPublicKey string) (*Gateway, error) {
	var err error
	readPayload := gatewayRead{}
	sendPayload := gatewaySend{}

	// Init gateway
	gw := Gateway{
		botToken:      botToken,
		botAppId:      botAppId,
		botPublicKey:  botPublicKey,
		userOccupancy: cmap.New[string](),
		guildStates:   cmap.New[*GuildState](),
	}

	// Connect to Discord websocket
	dialCtx, dialCancel := context.WithTimeout(rootctx, DefaultTimeout)
	gw.ws, _, err = websocket.Dial(dialCtx, DiscordWSS, nil)
	dialCancel()
	if err != nil {
		return nil, err
	}
	defer func() {
		if gw.State == GwClosed {
			gw.ws.Close(websocket.StatusInternalError, "")
		}
	}()

	// Receive HELLO event
	if err = read(gw.ws, rootctx, &readPayload); err != nil {
		return nil, err
	}
	helloData := helloData{}
	json.Unmarshal(readPayload.D, &helloData)

	// Store hb interval
	gw.heartbeatIntv = helloData.Interval

	// Send IDENTIFY event
	idData := identifyData{
		Token:      gw.botToken,
		Intents:    GATEWAY_INTENTS,
		Properties: GATEWAY_PROPERTIES,
	}
	sendPayload.Op = Identify
	sendPayload.D, _ = json.Marshal(&idData)
	if err = send(gw.ws, rootctx, &sendPayload); err != nil {
		return nil, err
	}

	// Receive READY or INVALID_SESSION event
	if err = read(gw.ws, rootctx, &readPayload); err != nil {
		return nil, err
	}
	if readPayload.Op == InvalidSession {
		return nil,
			errors.New("received INVALID_SESSION after IDENTIFY")
	}
	readyData := readyData{}
	json.Unmarshal(readPayload.D, &readyData)

	// Store session resume data
	gw.sessionId = readyData.SessionId
	gw.resumeUrl = readyData.ResumeUrl
	gw.lastSeq = readPayload.S

	// Change to READY state
	gw.State = GwReady
	return &gw, nil
}

func (gw *Gateway) Listen(rootctx context.Context) error {
	var err error
	readPayload := gatewayRead{}
	sendPayload := gatewaySend{}

	// If resume loop ends, close gateway
	defer func() {
		gw.State = GwClosed
		gw.ws.Close(websocket.StatusNormalClosure, "")
	}()

	// Enter resume loop
	for {
		// Start heartbeat
		gwCtx, gwCancel := context.WithCancel(rootctx)
		go heartbeat(gw, gwCtx)

		// Enter read loop
		keepReading := true
		for keepReading {
			if err = read(gw.ws, gwCtx, &readPayload); err != nil {
				log.Println("gw read err:", err)
				keepReading = false
				break
			}

			// Store sequence number
			gw.lastSeq = readPayload.S

			// Handle event according to opcode
			switch readPayload.Op {
			case Heartbeat:
				// Send heartbeat
				sendPayload.Op = Heartbeat
				sendPayload.D, _ = json.Marshal(gw.lastSeq)
				err = send(gw.ws, gwCtx, &sendPayload)
				if err != nil {
					keepReading = false
				}
			case Reconnect:
				// Close with ServiceRestart to trigger resume
				// Errors on next read or send
				gw.ws.Close(websocket.StatusServiceRestart, "")
			case InvalidSession:
				// Close with InvalidSession to avoid resume
				// Errors on next read or send
				gw.ws.Close(StatusGatewayInvalidSession, "")
			case Dispatch:
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
		gw.ws.Close(websocket.StatusServiceRestart, "")

		// Connect to resume url
		dialCtx, dialCancel := context.WithTimeout(
			rootctx, DefaultTimeout)
		gw.ws, _, err = websocket.Dial(dialCtx, gw.resumeUrl, nil)
		dialCancel()
		if err != nil {
			return err
		}

		// Receive HELLO event
		if err = read(gw.ws, rootctx, &readPayload); err != nil {
			return err
		}
		helloData := helloData{}
		json.Unmarshal(readPayload.D, &helloData)

		// Store hb interval
		gw.heartbeatIntv = helloData.Interval

		// Send RESUME event
		resumeData := resumeData{
			Token:     gw.botToken,
			SessionId: gw.sessionId,
			S:         gw.lastSeq,
		}
		sendPayload.Op = Resume
		sendPayload.D, _ = json.Marshal(&resumeData)
		if err = send(gw.ws, rootctx, &sendPayload); err != nil {
			return err
		}

		// Change to READY state
		gw.State = GwReady
	}
}

func (gw *Gateway) JoinChannel(rootctx context.Context, guildId string, channelId *string) error {
	// Lock before any new guild states are created
	gw.guildStatesLock.Lock()

	// Check if bot is already in channel
	guild, ok := gw.guildStates.Get(guildId)
	if ok && isIdEqual(guild.botChnlId, channelId) {
		gw.guildStatesLock.Unlock()
		return nil
	}

	// Init guild state if doesn't exist
	if !ok {
		guild = &GuildState{}
		guild.guildId = guildId
		guild.joinedChnl = make(chan *string)
		gw.guildStates.Set(guildId, guild)
	}
	gw.guildStatesLock.Unlock()

	// Lock guild to prevent JoinChannel()
	// from executing in another thread
	guild.joinLock.Lock()
	defer guild.joinLock.Unlock()

	// Send a voice state update
	payload := gatewaySend{Op: VoiceStateUpdate}
	data := voiceStateUpdateData{
		guildId, channelId, false, false,
	}
	payload.D, _ = json.Marshal(&data)
	err := send(gw.ws, rootctx, &payload)
	if err != nil {
		return err
	}

	// Wait for channel join or context cancel
	select {
	case joinedId := <-guild.joinedChnl:
		if !isIdEqual(joinedId, channelId) {
			return errors.New("unable to join channel")
		}
	case <-rootctx.Done():
		return rootctx.Err()
	}

	return nil
}

func (gw *Gateway) PlayAudio(guildId string) error {
	return nil
}

func handleDispatch(gw *Gateway, ctx context.Context, payload *gatewayRead) error {
	switch payload.T {
	case "VOICE_STATE_UPDATE":
		// Get channel, user, and guild
		voiceData := voiceStateData{}
		json.Unmarshal(payload.D, &voiceData)
		gw.userOccupancy.Set(
			voiceData.GuildId+voiceData.UserId,
			voiceData.ChannelId)
		// Return if not related to bot
		if voiceData.UserId != gw.botAppId {
			return nil
		}
		log.Printf("voice state updt: guild: %s chnl: %s\n",
			voiceData.GuildId, voiceData.ChannelId)
		// I think guild state should always be init'd by the time
		// this event is received, so ignore event if not init'd
		guild, ok := gw.guildStates.Get(voiceData.GuildId)
		if !ok {
			return nil
		}
		// Store data in guild state
		guild.botChnlId = &voiceData.ChannelId
		if voiceData.ChannelId == "" {
			guild.botChnlId = nil
		}
		guild.voiceSessId = voiceData.SessionId
		guild.freshChnlSess = true
		// If chnl id is nil, make sure voice gw is closed
		if guild.botChnlId == nil {
			if guild.voiceGw != nil {
				guild.voiceGw.Close(ctx)
			}
			notifyJoin(guild, nil)
			// Join voice gateway with new server data
		} else if guild.freshTokEnd {
			if err := startVoiceGw(gw, guild, ctx); err != nil {
				log.Println("voice gw err:", err)
			}
		}
	case "VOICE_SERVER_UPDATE":
		// Get new voice server token and endpoint
		serverData := voiceServerUpdateData{}
		json.Unmarshal(payload.D, &serverData)
		log.Printf("voice server updt: guild: %s token: %s url: %s\n",
			serverData.GuildId, serverData.Token, serverData.Endpoint)
		// I think guild state should always be init'd by the time
		// this event is received, so ignore event if not init'd
		guild, ok := gw.guildStates.Get(serverData.GuildId)
		if !ok {
			return nil
		}
		// Store data in guild state
		guild.voiceEndpoint = "wss://" + serverData.Endpoint + "?v=4"
		guild.voiceToken = serverData.Token
		guild.freshTokEnd = true
		// Join voice gateway with new session and non-null channel
		if guild.freshChnlSess && guild.botChnlId != nil {
			if err := startVoiceGw(gw, guild, ctx); err != nil {
				log.Println("voice gw err:", err)
			}
		}
	case "RESUMED":
		// Do nothing
	case "VOICE_CHANNEL_STATUS_UPDATE":
		// Do nothing
	case "INTERACTION_CREATE":
		interactionData := InteractionData{}
		json.Unmarshal(payload.D, &interactionData)
		switch interactionData.Data.Name {
		case "play":
			go play(gw, ctx, interactionData)
		default:
			log.Println(
				"unhandled interaction:",
				interactionData.Data.Name)
		}
	default:
		log.Println("unhandled dispatch:", payload.T)
	}
	return nil
}

func startVoiceGw(gw *Gateway, guild *GuildState, ctx context.Context) error {
	// Close voice gw if not already closed
	if guild.voiceGw != nil {
		guild.voiceGw.Close(ctx)
	}

	// Create voice gateway
	var err error
	guild.voiceGw, err = VoiceConnect(
		ctx,
		gw.botAppId,
		guild.guildId,
		guild.voiceSessId,
		guild.voiceToken,
		guild.voiceEndpoint,
	)

	// Set to stale so that next startVoiceGw
	// is not triggered before getting
	// a Voice State/Server Update event
	guild.freshTokEnd = false
	guild.freshTokEnd = false

	// Send signal to JoinChannel
	if err != nil {
		notifyJoin(guild, nil)
		return err
	}

	// Start listening in thread
	notifyJoin(guild, guild.botChnlId)
	go guild.voiceGw.Listen(ctx)
	return nil
}

func notifyJoin(guild *GuildState, channelId *string) {
	// Do a non-blocking write to the Guild notification
	// channel for any listening JoinChannel coroutines
	select {
	case guild.joinedChnl <- channelId:
		// Do nothing
	default:
		// Do nothing
	}
}

func isIdEqual(id1 *string, id2 *string) bool {
	if id1 == id2 {
		return true
	}
	if id1 != nil && id2 != nil {
		return *id1 == *id2
	}
	return false
}

func heartbeat(gw *Gateway, ctx context.Context) error {
	heartbeat := gatewaySend{Op: Heartbeat}
	for {
		heartbeat.D, _ = json.Marshal(gw.lastSeq)
		if err := send(gw.ws, ctx, &heartbeat); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(
			time.Duration(gw.heartbeatIntv) * time.Millisecond):
		}
	}
}

func read(c *websocket.Conn, ctx context.Context, payload *gatewayRead) error {
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
	if payload.Op != HeartbeatAck {
		log.Printf("read: op: %s s: %d t: %s\n",
			OpcodeNames[payload.Op], payload.S, payload.T)
	}
	return err
}

func send(c *websocket.Conn, ctx context.Context, payload *gatewaySend) error {
	encoded, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	err := c.Write(ctx, websocket.MessageText, encoded)
	if payload.Op != opcode(Heartbeat) {
		log.Println("send: op:", OpcodeNames[payload.Op])
	}
	return err
}
