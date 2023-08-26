package internal

import (
	"context"
	"encoding/json"
	"encoding/binary"
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"net"
	"os"
	"io"

	"nhooyr.io/websocket"
	"golang.org/x/crypto/nacl/secretbox"
)

const discordWSS = "wss://gateway.discord.gg"

const defaultTimeout = 2 * time.Minute
const connectVoiceTimeout = 10 * time.Second
const changeChannelTimeout = 10 * time.Second
const packetSendTimeout = 5 * time.Second
const silenceTimeout = 5 * time.Second

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
const packetDuration = 19900 * time.Microsecond
const burstDuration = packetBurst * packetDuration

type gwState int

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
	lastSeq         int
	resumeUrl       string
	sessionId       string
	heartbeatIntv   int
}

type vGwState int

const (
	vGwClosed vGwState = iota
	vGwReady
	vGwResuming
)

type guildState struct {
	guildId       string
	chnlId        string
	botAppId      string
	freshTokEnd   bool
	freshChnlSess bool
	joinedChnl    chan string
	joinLock      sync.Mutex
	playLock      sync.Mutex
	isPlaying     bool
	// Voice gateway related
	vState         vGwState
	vWs            *websocket.Conn
	vSessId        string
	vToken         string
	vEndpoint      string
	vPackets       chan []byte
	vPackAck       chan bool
	vHeartbeatIntv int
	vSsrc          uint32
	vUrl           string
	vSecretKey     [32]byte
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
		cmd:           make(chan InteractionData),
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
	if err = read(rootctx, gw.ws, &readPayload); err != nil {
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
	if err = send(rootctx, gw.ws, &sendPayload); err != nil {
		return err
	}

	// Receive READY or INVALID_SESSION event
	if err = read(rootctx, gw.ws, &readPayload); err != nil {
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
		go gwHeartbeat(hbCtx, gw)

		// Enter read loop
		isReading := true
		for isReading {
			if err = read(rootctx, gw.ws, &readPayload); err != nil {
				slog.Error("gw read fail", "e", err)
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
				err = send(rootctx, gw.ws, &sendPayload)
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
				err = handleDispatch(rootctx, gw, &readPayload)
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
		slog.Info("gw close", "code", status)
		canResume, exists := validResumeCodes[status]
		if !canResume && exists {
			slog.Error("gw resume unable")
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
		if err = read(rootctx, gw.ws, &readPayload); err != nil {
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
		if err = send(rootctx, gw.ws, &sendPayload); err != nil {
			return err
		}

		// Change to READY state
		gw.state = gwReady
	}
}

func (gw *Gateway) ChangeChannel(rootctx context.Context, guildId string, channelId string) error {
	// Check if bot is already in channel
	// or if attempting to leave channel
	// when it never joined
	gw.guildStatesLock.Lock()
	guild, ok := gw.guildStates[guildId]
	if (ok && guild.chnlId == channelId) ||
		(!ok && channelId == NullChannelId) {
		gw.guildStatesLock.Unlock()
		return nil
	}

	// Init guild state if doesn't exist
	if !ok {
		guild = &guildState{}
		guild.guildId = guildId
		guild.botAppId = gw.botAppId
		guild.joinedChnl = make(chan string)
		guild.vPackets = make(chan []byte)
		guild.vPackAck = make(chan bool)
		gw.guildStates[guildId] = guild
	}
	gw.guildStatesLock.Unlock()

	// Lock guild to prevent ChangeChannel()
	// from executing in another thread
	guild.joinLock.Lock()
	defer guild.joinLock.Unlock()

	// Send a voice state update
	payload := gatewaySend{Op: voiceStateUpdate}
	data := voiceStateUpdateData{
		guildId, nil, false, true,
	}
	if channelId != NullChannelId {
		data.ChannelId = &channelId
	}
	payload.D, _ = json.Marshal(&data)
	err := send(rootctx, gw.ws, &payload)
	if err != nil {
		return err
	}

	// Wait for channel join, context cancel, or timeout
	timer := time.NewTimer(changeChannelTimeout)
	defer timer.Stop()
	select {
	case joinedId := <-guild.joinedChnl:
		if joinedId != channelId {
			return errors.New("gw not in channel")
		}
	case <-timer.C:
		return errors.New("gw join timeout")
	case <-rootctx.Done():
		return rootctx.Err()
	}
	return nil
}

func (gw *Gateway) PlayAudio(rootctx context.Context, guildId, songPath string) error {
	// Get guild state
	guild, ok := getGuildState(gw, guildId)
	if !ok {
		return errors.New("gw not in guild")
	}

	// Lock guild to prevent PlayAudio()
	// from executing in another thread
	guild.playLock.Lock()
	defer guild.playLock.Unlock()

	// Check if bot is in channel
	if guild.chnlId == NullChannelId {
		return errors.New("gw not in channel")
	}

	// Start playing
	guild.isPlaying = true
	defer func() {
		// Print newline to preserve min, avg,
		// and max printouts
		fmt.Print("\n")
		guild.isPlaying = false
	}()

	// Open song
	f, err := os.Open(songPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Init timer and stop it immediately
	timer := time.NewTimer(packetSendTimeout)
	defer timer.Stop()
	timer.Stop()

	// https://datatracker.ietf.org/doc/html/rfc3533#section-6
	headerBuf := [pageHeaderLen]byte{}
	segTable := [maxSegTableLen]byte{}
	packetBuf := [maxPacketLen]byte{}
	pLen := 0
	pNum := 0
	pStart := 0
	discard := 0

	// Init packet period tracking vars
	prevt := time.Now()
	var min int64 = prevt.Unix()
	var max, avg, samps int64

	// Parse Opus pages
	for guild.isPlaying {
		// Parse page header
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

		// Discard first two pages since these
		// contain metadata
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

		// Parse packets
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
				// Copy packet to new buffer 
				// and send to vUdp
				packet := make([]byte, pLen)
				copy(packet, packetBuf[:pLen])
				pLen = 0
				pStart = 0
				pNum += 1
				timer.Reset(packetSendTimeout)
				select {
				case guild.vPackets <- packet:
					ok := <-guild.vPackAck
					if !ok {
						return errors.New("packet send fail")
					}
				case <-timer.C:
					return errors.New("packet send timeout")
				case <-rootctx.Done():
					return rootctx.Err()
				}
				timer.Stop()

				// Wait for remaining burst duration after
				// sending packetBurst packets
				if pNum == packetBurst {
					dt := burstDuration - time.Since(prevt)
					if dt > 0 {
						time.Sleep(dt)
					}
					dt64 := time.Since(prevt).Microseconds()
					if dt64 < min {
						min = dt64
					}
					if dt64 > max {
						max = dt64
					}
					avg = (samps*avg + dt64)/(samps+1)
					samps++
					fmt.Printf(
						"min=%6d avg=%6d max=%6d\r",
						min, avg, max,
					)
					prevt = time.Now()
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

func (gw *Gateway) StopAudio(guildId string) error {
	// Get guild state
	guild, ok := getGuildState(gw, guildId)
	if !ok {
		return errors.New("gw not in guild")
	}

	// Check if bot is in channel
	if guild.chnlId == NullChannelId {
		return errors.New("gw not in channel")
	}

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
		vClose(guild)
		guild.freshTokEnd = false
		guild.freshChnlSess = false
	}
	gw.ws.Close(websocket.StatusNormalClosure, "")
}

func handleDispatch(ctx context.Context, gw *Gateway, payload *gatewayRead) error {
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
		guild.chnlId = voiceData.ChannelId
		guild.vSessId = voiceData.SessionId
		guild.freshChnlSess = true
		// If chnl id is "", make sure voice gw is closed
		if guild.chnlId == NullChannelId {
			vClose(guild)
			notifyJoin(guild, NullChannelId)
			// Join voice gateway with new server data
		} else if guild.freshTokEnd {
			startVoiceGw(ctx, guild)
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
		guild.vEndpoint = "wss://" + serverData.Endpoint + "?v=4"
		guild.vToken = serverData.Token
		guild.freshTokEnd = true
		// Join voice gateway with new session and non-null channel
		if guild.freshChnlSess && guild.chnlId != NullChannelId {
			startVoiceGw(ctx, guild)
		}
	case "INTERACTION_CREATE":
		interData := InteractionData{}
		json.Unmarshal(payload.D, &interData)
		key := interData.GuildId + interData.Member.User.Id
		gw.userOccLock.RLock()
		interData.ChnlId = gw.userOccupancy[key]
		gw.userOccLock.RUnlock()
		select {
		case gw.cmd <- interData:
			// Successfully passed command
		default:
			// Do nothing
		}
	case "VOICE_CHANNEL_EFFECT_SEND":
		// Do nothing
	case "RESUMED":
		// Do nothing
	case "VOICE_CHANNEL_STATUS_UPDATE":
		// Do nothing
	default:
		slog.Error("gw unknown dispatch", "type", payload.T)
	}
	return nil
}

func getGuildState(gw *Gateway, guildId string) (*guildState, bool) {
	gw.guildStatesLock.RLock()
	defer gw.guildStatesLock.RUnlock()
	guild, ok := gw.guildStates[guildId]
	return guild, ok
}

func startVoiceGw(ctx context.Context, guild *guildState) {
	// Close voice gw if not already closed
	vClose(guild)

	// Set to stale so that next startVoiceGw
	// is not triggered before getting
	// a Voice State/Server Update event
	guild.freshChnlSess = false
	guild.freshTokEnd = false

	go func() {
		connCtx, connCancel := context.WithTimeout(
			ctx, connectVoiceTimeout)
		defer connCancel()

		// Create voice gateway
		err := vConnect(connCtx, guild)
		if err != nil {
			notifyJoin(guild, NullChannelId)
			return
		}

		// Start listening
		notifyJoin(guild, guild.chnlId)
		vServe(ctx, guild)
	}()
}

func notifyJoin(guild *guildState, channelId string) {
	// Do a non-blocking write to the Guild notification
	// channel for any listening ChangeChannel coroutines
	select {
	case guild.joinedChnl <- channelId:
		// Do nothing
	default:
		// Do nothing
	}
}

func gwHeartbeat(ctx context.Context, gw *Gateway) error {
	heartbeat := gatewaySend{Op: heartbeat}
	interval := time.Duration(gw.heartbeatIntv) * time.Millisecond
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		heartbeat.D, _ = json.Marshal(gw.lastSeq)
		if err := send(ctx, gw.ws, &heartbeat); err != nil {
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

func read(ctx context.Context, c *websocket.Conn, payload *gatewayRead) error {
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
		slog.Debug(
			"gw read",
			"op", opcodeNames[payload.Op],
			"s", payload.S, "t", payload.T,
		)
	}
	return err
}

func send(ctx context.Context, c *websocket.Conn, payload *gatewaySend) error {
	encoded, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	err := c.Write(ctx, websocket.MessageText, encoded)
	if payload.Op != opcode(heartbeat) {
		slog.Debug("gw send", "op", opcodeNames[payload.Op])
	}
	return err
}

func vConnect(ctx context.Context, guild *guildState) error {
	var err error
	payload := voiceGwPayload{}

	// Connect to Discord websocket
	dialCtx, dialCancel := context.WithTimeout(ctx, defaultTimeout)
	guild.vWs, _, err = websocket.Dial(dialCtx, guild.vEndpoint, nil)
	dialCancel()
	if err != nil {
		return err
	}
	defer func() {
		if guild.vState == vGwClosed {
			guild.vWs.Close(websocket.StatusInternalError, "")
		}
	}()

	// Receive HELLO event
	if err = vRead(ctx, guild.vWs, &payload); err != nil {
		return err
	}
	helloData := voiceHelloData{}
	json.Unmarshal(payload.D, &helloData)

	// Store hb interval
	guild.vHeartbeatIntv = int(helloData.Interval)

	// Send IDENTIFY event
	idData := voiceIdentifyData{
		ServerId:  guild.guildId,
		UserId:    guild.botAppId,
		SessionId: guild.vSessId,
		Token:     guild.vToken,
	}
	payload.Op = voiceIdentify
	payload.D, _ = json.Marshal(&idData)
	if err = vSend(ctx, guild.vWs, &payload); err != nil {
		return err
	}

	// Receive READY event
	if err = vRead(ctx, guild.vWs, &payload); err != nil {
		return err
	}
	readyData := voiceReadyData{}
	json.Unmarshal(payload.D, &readyData)

	// Store voice UDP data and ssrc
	guild.vSsrc = readyData.Ssrc
	guild.vUrl = fmt.Sprintf(
		"%s:%d", readyData.Ip, readyData.Port,
	)

	// Send SELECT PROTOCOL event
	if err = vSend(ctx, guild.vWs, &cachedSelectPrtcl); err != nil {
		return err
	}

	// Receive description
	// Sometimes opcodes 18, 20 (unknown), or 5 (speaking) are sent
	for payload.Op != voiceSessDesc {
		if err = vRead(ctx, guild.vWs, &payload); err != nil {
			return err
		}
	}
	sessData := voiceSessionDesc{}
	json.Unmarshal(payload.D, &sessData)

	// Store secret key
	json.Unmarshal(sessData.SecretKey, &guild.vSecretKey)

	// Change to READY state
	guild.vState = vGwReady
	return nil
}

func vServe(ctx context.Context, guild *guildState) error {
	var err error
	payload := voiceGwPayload{}

	// If resume loop ends, close gateway
	defer vClose(guild)

	// Enter resume loop
	for {
		// Start heartbeat
		gwCtx, gwCancel := context.WithCancel(ctx)
		go vHeartbeat(gwCtx, guild)
		go vUdp(gwCtx, guild)

		// Enter read loop
		isReading := true
		for isReading {
			if err = vRead(gwCtx, guild.vWs, &payload); err != nil {
				slog.Error(
					"voice read fail",
					"g", guild.guildId,
					"e", err,
				)
				isReading = false
				break
			}

			// Handle event according to opcode
			switch payload.Op {
			case voiceHeartbeat:
				// Send heartbeat
				err = vSend(gwCtx, guild.vWs, &cachedHeartbeat)
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
		guild.vState = vGwResuming
		gwCancel()

		// If root ctx cancelled, dont attempt resume
		if ctx.Err() != nil {
			return err
		}

		// Check if gateway can be resumed
		status := websocket.CloseStatus(err)
		slog.Info("voice close", "g", guild.guildId, "code", status)
		canResume, exists := voiceValidResumeCodes[status]
		if !canResume && exists {
			slog.Error(
				"voice resume unable",
				"g", guild.guildId,
			)
			return err
		}

		// Close websocket
		guild.vWs.Close(websocket.StatusServiceRestart, "")

		// Connect to resume url
		dialCtx, dialCancel := context.WithTimeout(
			ctx, defaultTimeout)
		newWs, _, err := websocket.Dial(dialCtx, guild.vEndpoint, nil)
		dialCancel()
		if err != nil {
			return err
		}
		guild.vWs = newWs

		// Receive HELLO event
		if err = vRead(gwCtx, guild.vWs, &payload); err != nil {
			return err
		}
		helloData := voiceHelloData{}
		json.Unmarshal(payload.D, &helloData)

		// Store hb interval
		guild.vHeartbeatIntv = int(helloData.Interval)

		// Send RESUME event
		resumeData := voiceResumeData{
			ServerId:  guild.guildId,
			SessionId: guild.vSessId,
			Token:     guild.vToken,
		}
		payload.Op = voiceResume
		payload.D, _ = json.Marshal(&resumeData)
		if err = vSend(ctx, guild.vWs, &payload); err != nil {
			return err
		}

		// Change to READY state
		guild.vState = vGwReady
	}
}

func vHeartbeat(ctx context.Context, guild *guildState) error {
	interval := time.Duration(guild.vHeartbeatIntv) * time.Millisecond
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		if err := vSend(ctx, guild.vWs, &cachedHeartbeat); err != nil {
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

func vUdp(ctx context.Context, guild *guildState) error {
	// Open voice udp socket
	sock, err := net.Dial("udp", guild.vUrl)
	if err != nil {
		return err
	}
	defer sock.Close()

	// Initialize speaking and silent payloads
	data, _ := json.Marshal(voiceSpeakingData{
		Speaking: 1,
		Delay:    0,
		Ssrc:     guild.vSsrc,
	})
	speakingPayload := voiceGwPayload{
		Op: voiceSpeaking, D: data,
	}
	data, _ = json.Marshal(voiceSpeakingData{
		Speaking: 0,
		Delay:    0,
		Ssrc:     guild.vSsrc,
	})
	silentPayload := voiceGwPayload{
		Op: voiceSpeaking, D: data,
	}

	// Init timer and stop it immediately
	timer := time.NewTimer(silenceTimeout)
	defer timer.Stop()
	timer.Stop()

	// xsalsa20_poly1305 stuff, see
	// https://github.com/bwmarrin/discordgo
	// https://discord.com/developers/docs/topics/
	// voice-connections#encrypting-and-sending-voice
	nonce := [nonceLen]byte{}
	header := [rtpHeaderLen]byte{}
	header[0] = 0x80
	header[1] = 0x78
	binary.BigEndian.PutUint32(header[8:], guild.vSsrc)

	var sequence uint16
	var timestamp uint32
	var isSpeaking bool

	for {
		select {
		case packet := <-guild.vPackets:
			if !isSpeaking {
				// Indicate speaking
				err = vSend(ctx, guild.vWs, &speakingPayload)
				if err != nil {
					guild.vPackAck<-false
					return err
				}
				isSpeaking = true
			}
			// more xsalsa20_poly1305 stuff
			binary.BigEndian.PutUint16(header[2:], sequence)
			binary.BigEndian.PutUint32(header[4:], timestamp)
			sequence += 1
			timestamp += 960
			copy(nonce[:], header[:])
			encrypted := secretbox.Seal(
				header[:],
				packet,
				&nonce,
				&guild.vSecretKey,
			)
			_, err := sock.Write(encrypted)
			if err != nil {
				guild.vPackAck<-false
				return err
			}
			guild.vPackAck<-true
			timer.Stop()
			timer.Reset(silenceTimeout)
		case <-timer.C:
			if isSpeaking {
				// Indicate silent
				err = vSend(ctx, guild.vWs, &silentPayload)
				if err != nil {
					return err
				}
				isSpeaking = false
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func vClose(guild *guildState) error {
	guild.vState = vGwClosed
	if guild.vWs != nil {
		guild.vWs.Close(websocket.StatusNormalClosure, "")
	}
	return nil
}

func vRead(ctx context.Context, c *websocket.Conn, payload *voiceGwPayload) error {
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	_, raw, err := c.Read(ctx)
	if err != nil {
		return err
	}
	json.Unmarshal(raw, payload) // Unhandled err
	if payload.Op != voiceHeartbeatAck {
		slog.Debug(
			"voice read",
			"op", voiceOpcodeNames[payload.Op],
		)
	}
	return err
}

func vSend(ctx context.Context, c *websocket.Conn, payload *voiceGwPayload) error {
	encoded, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	err := c.Write(ctx, websocket.MessageText, encoded)
	if payload.Op != voiceHeartbeat {
		slog.Debug(
			"voice send",
			"op", voiceOpcodeNames[payload.Op],
		)
	}
	return err
}
