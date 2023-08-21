package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

const discordWSS = "wss://gateway.discord.gg"
const defaultTimeout = 2 * time.Minute
const connectVoiceTimeout = 10 * time.Second
const joinChannelTimeout = 10 * time.Second
const voicePacketTimeout = 5 * time.Second

const NullChannelId = ""

// Connect voice permission (1 << 20) ||
// Speak voice permission (1 << 21) ||
// GUILD_VOICE_STATES intent (1 << 7) = 3145856
const gatewayIntents = 1<<7 | 1<<20 | 1<<21

var gatewayProperties = identifyProperties{
	Os:      "linux",
	Browser: "disco",
	Device:  "lenovo thinkcentre",
}

const pageHeaderLen = 27
const maxSegTableLen = 255
const nonceLen = 24
const rtpHeaderLen = 12

// https://datatracker.ietf.org/doc/html/rfc3533#section-6
var magicStr = []byte("OggS")

// A packet is composed of at least one segment.
// A packet is terminated by a segment of length < 255.
// A segment of length = 255 indicates that a packet has only
// been partially read, and must be completed by appending
// the upcoming segments.
const partialPacketLen = 255

// 20 ms packet of 128 kbps opus audio is approximately
// 128000/8 * 20/1000 = 320 bytes. Apply a safety factor.
const maxPacketLen = 1024

// Number of packets to send consecutively without waiting
const packetBurst = 10

// Technically this should be 20 ms, but made it slightly
// shorter for better audio continuity
const packetDuration = 19800 * time.Microsecond

// Size of buffer channel for sending commands
const cmbBufLen = 10

type gwState int8

const (
	gwClosed gwState = iota
	gwReady
	gwResuming
)

type Gateway struct {
	state           gwState
	botToken        string
	botAppId        string
	botPublicKey    string
	songFolder      string
	userOccLock     sync.RWMutex
	userOccupancy   map[string]string
	guildStatesLock sync.RWMutex
	guildStates     map[string]*guildState
	cmd             chan InteractionData
	ws              *websocket.Conn
	lastSeq         int64
	resumeUrl       string
	sessionId       string
	heartbeatIntv   int64
}

type guildState struct {
	guildId       string
	botChnlId     string
	voiceSessId   string
	voiceToken    string
	voiceEndpoint string
	freshTokEnd   bool
	freshChnlSess bool
	voiceGw       *voiceGateway
	packets       chan []byte
	joinLock      chan bool
	playLock      chan bool
	joinedChnl    chan string
	isPlaying     bool
}

func Connect(rootctx context.Context, botToken, botAppId, botPublicKey, songFolder string) (*Gateway, error) {
	// Init gateway
	gw := Gateway{
		botToken:      botToken,
		botAppId:      botAppId,
		botPublicKey:  botPublicKey,
		songFolder:    songFolder,
		userOccupancy: make(map[string]string),
		guildStates:   make(map[string]*guildState),
		cmd:           make(chan InteractionData, cmbBufLen),
	}
	err := gw.Reconnect(rootctx)
	return &gw, err
}

func (gw *Gateway) Reconnect(rootctx context.Context) error {
	var err error
	readPayload := gatewayRead{}
	sendPayload := gatewaySend{}

	// Connect to Discord websocket
	dialCtx, dialCancel := context.WithTimeout(rootctx, defaultTimeout)
	gw.ws, _, err = websocket.Dial(dialCtx, discordWSS, nil)
	dialCancel()
	if err != nil {
		return err
	}
	defer func() {
		if gw.state == gwClosed {
			gw.ws.Close(websocket.StatusInternalError, "")
		}
	}()

	// Receive HELLO event
	if err = read(gw.ws, rootctx, &readPayload); err != nil {
		return err
	}
	helloData := helloData{}
	json.Unmarshal(readPayload.D, &helloData)

	// Store hb interval
	gw.heartbeatIntv = helloData.Interval

	// Send IDENTIFY event
	idData := identifyData{
		Token:      gw.botToken,
		Intents:    gatewayIntents,
		Properties: gatewayProperties,
	}
	sendPayload.Op = identify
	sendPayload.D, _ = json.Marshal(&idData)
	if err = send(gw.ws, rootctx, &sendPayload); err != nil {
		return err
	}

	// Receive READY or INVALID_SESSION event
	if err = read(gw.ws, rootctx, &readPayload); err != nil {
		return err
	}
	if readPayload.Op == invalidSession {
		return errors.New("received INVALID_SESSION after IDENTIFY")
	}
	readyData := readyData{}
	json.Unmarshal(readPayload.D, &readyData)

	// Store session resume data
	gw.sessionId = readyData.SessionId
	gw.resumeUrl = readyData.ResumeUrl
	gw.lastSeq = readPayload.S

	// Change to READY state
	gw.state = gwReady
	return nil
}

func (gw *Gateway) Serve(rootctx context.Context) error {
	var err error
	readPayload := gatewayRead{}
	sendPayload := gatewaySend{}

	// If resume loop ends, close gateway
	defer func() {
		gw.state = gwClosed
		gw.ws.Close(websocket.StatusNormalClosure, "")
	}()

	// Enter resume loop
	for {
		// Start heartbeat
		hbCtx, hbCancel := context.WithCancel(rootctx)
		go gwHeartbeat(gw, hbCtx)

		// Enter read loop
		isReading := true
		for isReading {
			if err = read(gw.ws, rootctx, &readPayload); err != nil {
				log.Println("gw read err:", err)
				isReading = false
				break
			}

			// Store sequence number
			gw.lastSeq = readPayload.S

			// Handle event according to opcode
			switch readPayload.Op {
			case heartbeat:
				// Send heartbeat
				sendPayload.Op = heartbeat
				sendPayload.D, _ = json.Marshal(gw.lastSeq)
				err = send(gw.ws, rootctx, &sendPayload)
				if err != nil {
					isReading = false
				}
			case reconnect:
				// Close with ServiceRestart to trigger resume
				// Errors on next read or send
				gw.ws.Close(websocket.StatusServiceRestart, "")
			case invalidSession:
				// Close with invalidSession to avoid resume
				// Errors on next read or send
				gw.ws.Close(statusGatewayInvalidSession, "")
			case dispatch:
				// Handle dispatch
				err = handleDispatch(gw, rootctx, &readPayload)
				if err != nil {
					isReading = false
				}
			}
		}

		// Change to Resuming state
		// Cancel heartbeat
		gw.state = gwResuming
		hbCancel()

		// If root ctx cancelled, dont attempt resume
		if rootctx.Err() != nil {
			return err
		}

		// Check if gateway can be resumed
		status := websocket.CloseStatus(err)
		log.Printf("close code: %d\n", status)
		canResume, exists := validResumeCodes[status]
		if !canResume && exists {
			log.Println("can't resume")
			return err
		}

		// Close websocket
		gw.ws.Close(websocket.StatusServiceRestart, "")

		// Connect to resume url
		dialCtx, dialCancel := context.WithTimeout(
			rootctx, defaultTimeout)
		newWs, _, err := websocket.Dial(dialCtx, gw.resumeUrl, nil)
		dialCancel()
		if err != nil {
			return err
		}
		gw.ws = newWs

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
		sendPayload.Op = resume
		sendPayload.D, _ = json.Marshal(&resumeData)
		if err = send(gw.ws, rootctx, &sendPayload); err != nil {
			return err
		}

		// Change to READY state
		gw.state = gwReady
	}
}

func (gw *Gateway) JoinChannel(rootctx context.Context, guildId string, channelId string) error {
	// Check if bot is already in channel
	gw.guildStatesLock.Lock()
	guild, ok := gw.guildStates[guildId]
	if ok && guild.botChnlId == channelId {
		gw.guildStatesLock.Unlock()
		return nil
	}

	// Init guild state if doesn't exist
	if !ok {
		guild = &guildState{}
		guild.guildId = guildId
		guild.packets = make(chan []byte)
		guild.joinLock = make(chan bool, 1)
		guild.joinLock <- true
		guild.playLock = make(chan bool, 1)
		guild.playLock <- true
		guild.joinedChnl = make(chan string)
		gw.guildStates[guildId] = guild
	}
	gw.guildStatesLock.Unlock()

	// Lock guild to prevent JoinChannel()
	// from executing in another thread
	select {
	case <-guild.joinLock:
		// Obtained lock
	case <-rootctx.Done():
		return errors.New("could not lock")
	}
	defer func() { guild.joinLock <- true }()

	// Send a voice state update
	payload := gatewaySend{Op: voiceStateUpdate}
	data := voiceStateUpdateData{
		guildId, nil, false, true,
	}
	if channelId != NullChannelId {
		data.ChannelId = &channelId
	}
	payload.D, _ = json.Marshal(&data)
	err := send(gw.ws, rootctx, &payload)
	if err != nil {
		return err
	}

	// Wait for channel join or context cancel/timeout
	select {
	case joinedId := <-guild.joinedChnl:
		if joinedId != channelId {
			return errors.New("unable to join channel")
		}
	case <-time.After(joinChannelTimeout):
		return errors.New("channel join timeout")
	case <-rootctx.Done():
		return rootctx.Err()
	}

	return nil
}

func (gw *Gateway) PlayAudio(rootctx context.Context, guildId, song string) error {
	// Get guild state
	guild, ok := getGuildState(gw, guildId)
	if !ok {
		return errors.New("bot not in guild")
	}

	// Lock guild to prevent PlayAudio()
	// from executing in another thread
	select {
	case <-guild.playLock:
		// Obtained lock
	case <-rootctx.Done():
		return errors.New("could not lock")
	}
	defer func() {
		guild.isPlaying = false
		guild.playLock <- true
	}()
	guild.isPlaying = true

	// Check if voice is connected
	if guild.voiceGw == nil {
		return errors.New("voice gateway not connected")
	} else if guild.voiceGw.state != vGwReady {
		return errors.New("voice gateway not connected")
	}

	// Open song
	f, err := os.Open(song)
	if err != nil {
		return err
	}
	defer f.Close()

	// https://datatracker.ietf.org/doc/html/rfc3533#section-6
	headerBuf := [pageHeaderLen]byte{}
	segTable := [maxSegTableLen]byte{}
	packetBuf := [maxPacketLen]byte{}
	pLen := 0
	pNum := 0
	pStart := 0
	discard := 0
	for guild.isPlaying {
		n, err := io.ReadFull(f, headerBuf[:])
		if err == io.EOF || n < pageHeaderLen {
			break
		}
		if !bytes.Equal(magicStr, headerBuf[:4]) {
			break
		}
		tableLen := int(headerBuf[26])
		_, err = io.ReadFull(f, segTable[:tableLen])
		if err != nil {
			return err
		}
		if discard < 2 {
			var sum int64
			for _, v := range segTable[:tableLen] {
				sum += int64(v)
			}
			_, err := f.Seek(sum, io.SeekCurrent)
			if err != nil {
				return err
			}
			discard += 1
			continue
		}
		for i := 0; i < tableLen; i++ {
			segLen := int(segTable[i])
			_, err = io.ReadFull(f, packetBuf[pStart:pStart+segLen])
			if err != nil {
				return err
			}
			pLen += segLen
			if segLen == partialPacketLen {
				pStart += partialPacketLen
			} else {
				packet := make([]byte, pLen)
				copy(packet, packetBuf[:pLen])
				pLen = 0
				pStart = 0
				pNum += 1
				select {
				case guild.packets <- packet:
					// Successfully sent packet to voice gw
				case <-time.After(voicePacketTimeout):
					return errors.New("packet send timeout")
				case <-rootctx.Done():
					return rootctx.Err()
				}
				if pNum == packetBurst {
					time.Sleep(packetDuration * packetBurst)
					pNum = 0
				}
			}
			// Break if StopAudio sets isPlaying to false
			if !guild.isPlaying {
				break
			}
		}
	}
	return nil
}

func (gw *Gateway) StopAudio(rootctx context.Context, guildId string) error {
	// Get guild state
	guild, ok := getGuildState(gw, guildId)
	if !ok {
		return errors.New("bot not in guild")
	}

	// Stop PlayAudio loop
	guild.isPlaying = false

	return nil
}

func (gw *Gateway) Cmd() <-chan InteractionData {
	return gw.cmd
}

func (gw *Gateway) Close() {
	gw.state = gwClosed
	gw.guildStatesLock.Lock()
	defer gw.guildStatesLock.Unlock()
	for _, guild := range gw.guildStates {
		if guild.voiceGw != nil {
			guild.voiceGw.Close()
		}
		guild.freshTokEnd = false
		guild.freshChnlSess = false
	}
	gw.ws.Close(websocket.StatusNormalClosure, "")
}

func handleDispatch(gw *Gateway, ctx context.Context, payload *gatewayRead) error {
	switch payload.T {
	case "VOICE_STATE_UPDATE":
		// Get channel, user, and guild
		voiceData := voiceStateData{}
		json.Unmarshal(payload.D, &voiceData)
		gw.userOccLock.Lock()
		gw.userOccupancy[voiceData.GuildId+voiceData.UserId] =
			voiceData.ChannelId
		gw.userOccLock.Unlock()
		// Return if not related to bot
		if voiceData.UserId != gw.botAppId {
			return nil
		}
		// I think guild state should always be init'd by the time
		// this event is received, so ignore event if not init'd
		guild, ok := getGuildState(gw, voiceData.GuildId)
		if !ok {
			return nil
		}
		// Store data in guild state
		guild.botChnlId = voiceData.ChannelId
		guild.voiceSessId = voiceData.SessionId
		guild.freshChnlSess = true
		// If chnl id is "", make sure voice gw is closed
		if guild.botChnlId == NullChannelId {
			if guild.voiceGw != nil {
				guild.voiceGw.Close()
			}
			notifyJoin(guild, NullChannelId)
			// Join voice gateway with new server data
		} else if guild.freshTokEnd {
			startVoiceGw(guild, gw.botAppId, ctx)
		}
	case "VOICE_SERVER_UPDATE":
		// Get new voice server token and endpoint
		serverData := voiceServerUpdateData{}
		json.Unmarshal(payload.D, &serverData)
		// I think guild state should always be init'd by the time
		// this event is received, so ignore event if not init'd
		guild, ok := getGuildState(gw, serverData.GuildId)
		if !ok {
			return nil
		}
		// Store data in guild state
		guild.voiceEndpoint = "wss://" + serverData.Endpoint + "?v=4"
		guild.voiceToken = serverData.Token
		guild.freshTokEnd = true
		// Join voice gateway with new session and non-null channel
		if guild.freshChnlSess && guild.botChnlId != NullChannelId {
			startVoiceGw(guild, gw.botAppId, ctx)
		}
	case "INTERACTION_CREATE":
		interData := InteractionData{}
		json.Unmarshal(payload.D, &interData)
		key := interData.GuildId + interData.Member.User.Id
		gw.userOccLock.RLock()
		interData.ChnlId = gw.userOccupancy[key]
		gw.userOccLock.RUnlock()
		gw.cmd <- interData
	case "VOICE_CHANNEL_EFFECT_SEND":
		// Do nothing
	case "RESUMED":
		// Do nothing
	case "VOICE_CHANNEL_STATUS_UPDATE":
		// Do nothing
	default:
		log.Println("unhandled dispatch:", payload.T)
	}
	return nil
}

func getGuildState(gw *Gateway, guildId string) (*guildState, bool) {
	gw.guildStatesLock.RLock()
	defer gw.guildStatesLock.RUnlock()
	guild, ok := gw.guildStates[guildId]
	return guild, ok
}

func startVoiceGw(guild *guildState, botAppId string, ctx context.Context) {
	// Close voice gw if not already closed
	if guild.voiceGw != nil {
		guild.voiceGw.Close()
	}

	// Set to stale so that next startVoiceGw
	// is not triggered before getting
	// a Voice State/Server Update event
	guild.freshChnlSess = false
	guild.freshTokEnd = false

	go func() {
		connCtx, connCancel := context.WithTimeout(ctx, connectVoiceTimeout)
		defer connCancel()

		// Create voice gateway
		voiceGw, err := voiceConnect(
			connCtx,
			guild.packets,
			botAppId,
			guild.guildId,
			guild.voiceSessId,
			guild.voiceToken,
			guild.voiceEndpoint,
		)

		// Send signal to JoinChannel
		if err != nil {
			notifyJoin(guild, NullChannelId)
			return
		}
		guild.voiceGw = voiceGw
		notifyJoin(guild, guild.botChnlId)

		// Start listening
		guild.voiceGw.Serve(ctx)
	}()
}

func notifyJoin(guild *guildState, channelId string) {
	// Do a non-blocking write to the Guild notification
	// channel for any listening JoinChannel coroutines
	select {
	case guild.joinedChnl <- channelId:
		// Do nothing
	default:
		// Do nothing
	}
}

func gwHeartbeat(gw *Gateway, ctx context.Context) error {
	heartbeat := gatewaySend{Op: heartbeat}
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
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	_, raw, err := c.Read(ctx)
	if err != nil {
		return err
	}
	// Reset T and D fields since these are optional
	payload.T = ""
	payload.D = nil
	json.Unmarshal(raw, payload) // Unhandled err
	if payload.Op != heartbeatAck {
		log.Printf("read: op: %s s: %d t: %s\n",
			opcodeNames[payload.Op], payload.S, payload.T)
	}
	return err
}

func send(c *websocket.Conn, ctx context.Context, payload *gatewaySend) error {
	encoded, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	err := c.Write(ctx, websocket.MessageText, encoded)
	if payload.Op != opcode(heartbeat) {
		log.Println("send: op:", opcodeNames[payload.Op])
	}
	return err
}
