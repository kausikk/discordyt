package internal

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/orcaman/concurrent-map/v2"
	"golang.org/x/crypto/nacl/secretbox"
	"nhooyr.io/websocket"
)

const DiscordWSS = "wss://gateway.discord.gg"
const DefaultTimeout = 2 * time.Minute
const ConnectVoiceTimeout = 10 * time.Second
const JoinChannelTimeout = 10 * time.Second
const VoicePacketTimeout = 5 * time.Second

// Connect voice permission (1 << 20) ||
// Speak voice permission (1 << 21) ||
// GUILD_VOICE_STATES intent (1 << 7) = 3145856
const GatewayIntents = 1<<7 | 1<<20 | 1<<21

var GatewayProperties = identifyProperties{
	Os:      "linux",
	Browser: "disco",
	Device:  "lenovo thinkcentre",
}

const PageHeaderLen = 27
const MaxSegTableLen = 255
const NonceLen = 24
const RTPHeaderLen = 12

// https://datatracker.ietf.org/doc/html/rfc3533#section-6
var MagicStr = []byte("OggS")

// A packet is composed of at least one segment.
// A packet is terminated by a segment of length < 255.
// A segment of length = 255 indicates that a packet has only
// been partially read, and must be completed by appending
// the upcoming segments.
const PartialPacketLen = 255

// 20 ms packet of 128 kbps opus audio is approximately
// 128000/8 * 20/1000 = 320 bytes. Apply a safety factor.
const MaxPacketLen = 1024

// Number of packets to send consecutively without waiting
const PacketBurst = 10

// Technically this should be 20 ms, but made it slightly
// shorter for better audio continuity
const PacketDuration = 19700 * time.Microsecond

// Size of buffer channel for sending commands
const cmbBufLen = 1000

type GatewayState int8

const (
	GwClosed GatewayState = iota
	GwReady
	GwResuming
)

type Gateway struct {
	state           GatewayState
	botToken        string
	botAppId        string
	botPublicKey    string
	songFolder      string
	userOccupancy   cmap.ConcurrentMap[string, string]
	guildStatesLock sync.Mutex
	guildStates     map[string]*GuildState
	playCmd         chan InteractionData
	stopCmd         chan InteractionData
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
	joinLock      chan bool
	playLock      chan bool
	joinedChnl    chan *string
	voicePaks     chan []byte
}

func Connect(rootctx context.Context, botToken, botAppId, botPublicKey, songFolder string) (*Gateway, error) {
	// Init gateway
	gw := Gateway{
		botToken:      botToken,
		botAppId:      botAppId,
		botPublicKey:  botPublicKey,
		songFolder:    songFolder,
		userOccupancy: cmap.New[string](),
		guildStates:   make(map[string]*GuildState),
		playCmd:       make(chan InteractionData, cmbBufLen),
		stopCmd:       make(chan InteractionData, cmbBufLen),
	}
	err := gw.Reconnect(rootctx)
	return &gw, err
}

func (gw *Gateway) Reconnect(rootctx context.Context) error {
	var err error
	readPayload := gatewayRead{}
	sendPayload := gatewaySend{}

	// Connect to Discord websocket
	dialCtx, dialCancel := context.WithTimeout(rootctx, DefaultTimeout)
	gw.ws, _, err = websocket.Dial(dialCtx, DiscordWSS, nil)
	dialCancel()
	if err != nil {
		return err
	}
	defer func() {
		if gw.state == GwClosed {
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
		Intents:    GatewayIntents,
		Properties: GatewayProperties,
	}
	sendPayload.Op = Identify
	sendPayload.D, _ = json.Marshal(&idData)
	if err = send(gw.ws, rootctx, &sendPayload); err != nil {
		return err
	}

	// Receive READY or INVALID_SESSION event
	if err = read(gw.ws, rootctx, &readPayload); err != nil {
		return err
	}
	if readPayload.Op == InvalidSession {
		return errors.New("received INVALID_SESSION after IDENTIFY")
	}
	readyData := readyData{}
	json.Unmarshal(readPayload.D, &readyData)

	// Store session resume data
	gw.sessionId = readyData.SessionId
	gw.resumeUrl = readyData.ResumeUrl
	gw.lastSeq = readPayload.S

	// Change to READY state
	gw.state = GwReady
	return nil
}

func (gw *Gateway) Listen(rootctx context.Context) error {
	var err error
	readPayload := gatewayRead{}
	sendPayload := gatewaySend{}

	// If resume loop ends, close gateway
	defer func() {
		gw.state = GwClosed
		gw.ws.Close(websocket.StatusNormalClosure, "")
	}()

	// Enter resume loop
	for {
		// Start heartbeat
		hbCtx, hbCancel := context.WithCancel(rootctx)
		go heartbeat(gw, hbCtx)

		// Enter read loop
		keepReading := true
		for keepReading {
			if err = read(gw.ws, rootctx, &readPayload); err != nil {
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
				err = send(gw.ws, rootctx, &sendPayload)
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
				err = handleDispatch(gw, rootctx, &readPayload)
				if err != nil {
					keepReading = false
				}
			}
		}

		// Change to Resuming state
		// Cancel heartbeat
		gw.state = GwResuming
		hbCancel()

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
		gw.state = GwReady
	}
}

func (gw *Gateway) JoinChannel(rootctx context.Context, guildId string, channelId *string) error {
	// Check if bot is already in channel
	gw.guildStatesLock.Lock()
	guild, ok := gw.guildStates[guildId]
	if ok && isIdEqual(guild.botChnlId, channelId) {
		gw.guildStatesLock.Unlock()
		return nil
	}

	// Init guild state if doesn't exist
	if !ok {
		guild = &GuildState{}
		guild.guildId = guildId
		guild.joinLock = make(chan bool, 1)
		guild.joinLock <- true
		guild.playLock = make(chan bool, 1)
		guild.playLock <- true
		guild.joinedChnl = make(chan *string)
		guild.voicePaks = make(chan []byte)
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
	payload := gatewaySend{Op: VoiceStateUpdate}
	data := voiceStateUpdateData{
		guildId, channelId, false, false,
	}
	payload.D, _ = json.Marshal(&data)
	err := send(gw.ws, rootctx, &payload)
	if err != nil {
		return err
	}

	// Wait for channel join or context cancel/timeout
	select {
	case joinedId := <-guild.joinedChnl:
		if !isIdEqual(joinedId, channelId) {
			return errors.New("unable to join channel")
		}
	case <-time.After(JoinChannelTimeout):
		return errors.New("channel join timeout")
	case <-rootctx.Done():
		return rootctx.Err()
	}

	return nil
}

func (gw *Gateway) GetUserChannel(guildId, userId string) (string, bool) {
	return gw.userOccupancy.Get(guildId + userId)
}

func (gw *Gateway) PlayAudio(rootctx context.Context, guildId, song string) error {
	// Get guild state
	guild, ok := getGuildState(gw, guildId)
	if !ok {
		return errors.New("bot not in guild")
	}

	// Check if voice is connected
	if guild.voiceGw == nil {
		return errors.New("voice gateway not connected")
	} else if guild.voiceGw.State != VGwReady {
		return errors.New("voice gateway not connected")
	}

	// Lock guild to prevent PlayAudio()
	// from executing in another thread
	select {
	case <-guild.playLock:
		// Obtained lock
	case <-rootctx.Done():
		return errors.New("could not lock")
	}
	defer func() { guild.playLock <- true }()

	// Open song
	f, err := os.Open(song)
	if err != nil {
		return err
	}
	defer f.Close()

	// https://datatracker.ietf.org/doc/html/rfc3533#section-6
	headerBuf := [PageHeaderLen]byte{}
	segTable := [MaxSegTableLen]byte{}
	packetBuf := [MaxPacketLen]byte{}
	pLen := 0
	pNum := 0
	pStart := 0
	discard := 0
	for {
		n, err := io.ReadFull(f, headerBuf[:])
		if err == io.EOF || n < PageHeaderLen {
			break
		}
		tableLen := int(headerBuf[26])
		_, err = io.ReadFull(f, segTable[:tableLen])
		if err != nil {
			return err
		}
		if discard < 2 {
			s := sum(segTable[:tableLen])
			temp := make([]byte, s)
			_, err = io.ReadFull(f, temp)
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
			if segLen == PartialPacketLen {
				pStart += PartialPacketLen
			} else {
				packet := make([]byte, pLen)
				copy(packet, packetBuf[:pLen])
				pLen = 0
				pStart = 0
				pNum += 1
				select {
				case guild.voicePaks <- packet:
					// Do nothing
				case <-time.After(VoicePacketTimeout):
					return errors.New("packet send timeout")
				case <-rootctx.Done():
					return rootctx.Err()
				}
				if pNum == PacketBurst {
					time.Sleep(PacketDuration * PacketBurst)
					pNum = 0
				}
			}
		}
	}

	return nil
}

func (gw *Gateway) StopAudio(rootctx context.Context, guildId string) error {
	// Get guild state
	_, ok := getGuildState(gw, guildId)
	if !ok {
		return errors.New("bot not in guild")
	}

	// Send a voice state update to leave channel
	payload := gatewaySend{Op: VoiceStateUpdate}
	data := voiceStateUpdateData{
		guildId, nil, false, false,
	}
	payload.D, _ = json.Marshal(&data)
	return send(gw.ws, rootctx, &payload)
}

func (gw *Gateway) PlayCmd(rootctx context.Context) <-chan InteractionData {
	return gw.playCmd
}

func (gw *Gateway) StopCmd(rootctx context.Context) <-chan InteractionData {
	return gw.stopCmd
}

func (gw *Gateway) Close(rootctx context.Context) {
	gw.state = GwClosed
	gw.ws.Close(websocket.StatusNormalClosure, "")
	gw.guildStatesLock.Lock()
	defer gw.guildStatesLock.Unlock()
	for _, guild := range gw.guildStates {
		if guild.voiceGw != nil {
			guild.voiceGw.Close(rootctx)
		}
		guild.freshTokEnd = false
		guild.freshChnlSess = false
	}
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
		guild, ok := getGuildState(gw, voiceData.GuildId)
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
			startVoiceGw(guild, gw.botAppId, ctx)
		}
	case "VOICE_SERVER_UPDATE":
		// Get new voice server token and endpoint
		serverData := voiceServerUpdateData{}
		json.Unmarshal(payload.D, &serverData)
		log.Printf("voice server updt: guild: %s token: %s url: %s\n",
			serverData.GuildId, serverData.Token, serverData.Endpoint)
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
		if guild.freshChnlSess && guild.botChnlId != nil {
			startVoiceGw(guild, gw.botAppId, ctx)
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
		case "stop":
			go stop(gw, ctx, interactionData)
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

func getGuildState(gw *Gateway, guildId string) (*GuildState, bool) {
	gw.guildStatesLock.Lock()
	defer gw.guildStatesLock.Unlock()
	guild, ok := gw.guildStates[guildId]
	return guild, ok
}

func startVoiceGw(guild *GuildState, botAppId string, ctx context.Context) {
	// Close voice gw if not already closed
	if guild.voiceGw != nil {
		guild.voiceGw.Close(ctx)
	}

	// Set to stale so that next startVoiceGw
	// is not triggered before getting
	// a Voice State/Server Update event
	guild.freshChnlSess = false
	guild.freshTokEnd = false

	go func() {
		connCtx, connCancel := context.WithTimeout(ctx, ConnectVoiceTimeout)
		defer connCancel()

		// Create voice gateway
		var err error
		guild.voiceGw, err = VoiceConnect(
			connCtx,
			botAppId,
			guild.guildId,
			guild.voiceSessId,
			guild.voiceToken,
			guild.voiceEndpoint,
		)

		// Send signal to JoinChannel
		if err != nil {
			notifyJoin(guild, nil)
			return
		}
		notifyJoin(guild, guild.botChnlId)

		deadVoiceGw := make(chan bool)
		// Start thread for sending packets
		go startVoiceUdp(guild.voiceGw, ctx, guild.voicePaks, deadVoiceGw)
		// Start listening
		guild.voiceGw.Listen(ctx)
		// After voice gw closes, notify udp handler
		deadVoiceGw <- true
	}()
}

func startVoiceUdp(voiceGw *VoiceGateway, ctx context.Context, voicePaks <-chan []byte, deadVoiceGW <-chan bool) {
	// Open voice udp socket
	url := fmt.Sprintf(
		"%s:%d",
		voiceGw.ip, voiceGw.port,
	)
	sock, err := net.Dial("udp", url)
	if err != nil {
		return
	}
	defer sock.Close()

	err = voiceGw.Speaking(ctx, true)
	if err != nil {
		return
	}

	// xsalsa20_poly1305 stuff, see
	// https://github.com/bwmarrin/discordgo
	// https://discord.com/developers/docs/topics/
	// voice-connections#encrypting-and-sending-voice
	nonce := [NonceLen]byte{}
	header := make([]byte, RTPHeaderLen)
	header[0] = 0x80
	header[1] = 0x78
	binary.BigEndian.PutUint32(header[8:], voiceGw.ssrc)

	for {
		select {
		case packet := <-voicePaks:
			// more xsalsa20_poly1305 stuff
			binary.BigEndian.PutUint16(header[2:], voiceGw.sequence)
			binary.BigEndian.PutUint32(header[4:], voiceGw.timestamp)
			voiceGw.sequence += 1
			voiceGw.timestamp += 960
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
		case <-deadVoiceGW:
			return
		case <-ctx.Done():
			return
		}
	}
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

func sum(b []byte) int {
	var sum int = 0
	for _, v := range b {
		sum += int(v)
	}
	return sum
}
