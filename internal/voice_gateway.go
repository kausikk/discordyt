package internal

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/nacl/secretbox"
	"nhooyr.io/websocket"
)

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
	songCtrl      chan songFile
	songDone      chan songFile
	ws            *websocket.Conn
	heartbeatIntv int
	ssrc          uint32
	url           string
	secretKey     [32]byte
}

func voiceConnect(rootctx context.Context, songCtrl, songDone chan songFile, botAppId, guildId, sessionId, token, endpoint string) (*voiceGateway, error) {
	var err error
	payload := voiceGwPayload{}

	// Init gateway
	voiceGw := voiceGateway{
		botAppId:  botAppId,
		guildId:   guildId,
		sessionId: sessionId,
		token:     token,
		endpoint:  endpoint,
		songCtrl:  songCtrl,
		songDone:  songDone,
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
	voiceGw.heartbeatIntv = int(helloData.Interval)

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
	voiceGw.url = fmt.Sprintf(
		"%s:%d", readyData.Ip, readyData.Port,
	)

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
		go voiceGwHeartbeat(gwCtx, voiceGw)
		go voiceGwUdp(gwCtx, voiceGw)

		// Enter read loop
		isReading := true
		for isReading {
			if err = vRead(voiceGw.ws, gwCtx, &payload); err != nil {
				slog.Error(
					"voice read fail",
					"g", voiceGw.guildId,
					"e", err,
				)
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
		slog.Info("voice close", "g", voiceGw.guildId, "code", status)
		canResume, exists := voiceValidResumeCodes[status]
		if !canResume && exists {
			slog.Error(
				"voice resume unable",
				"g", voiceGw.guildId,
			)
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
		voiceGw.heartbeatIntv = int(helloData.Interval)

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

func voiceGwHeartbeat(ctx context.Context, voiceGw *voiceGateway) error {
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

func voiceGwUdp(ctx context.Context, voiceGw *voiceGateway) {
	// Define log
	log := func(err error) {
		slog.Error(
			"voice gw udp fail",
			"g", voiceGw.guildId,
			"e", err,
		)
	}
	// Open udp socket
	sock, err := net.Dial("udp", voiceGw.url)
	if err != nil {
		log(err)
		return
	}
	defer sock.Close()
	// Initialize speaking and silent payloads
	data, _ := json.Marshal(voiceSpeakingData{
		Speaking: 1,
		Delay:    0,
		Ssrc:     voiceGw.ssrc,
	})
	speakingPayload := voiceGwPayload{
		Op: voiceSpeaking, D: data,
	}
	data, _ = json.Marshal(voiceSpeakingData{
		Speaking: 0,
		Delay:    0,
		Ssrc:     voiceGw.ssrc,
	})
	silentPayload := voiceGwPayload{
		Op: voiceSpeaking, D: data,
	}
	// Initialize timestamp, opus buffer, encryption buffers
	// https://datatracker.ietf.org/doc/html/rfc3533#section-6
	// xsalsa20_poly1305 stuff, see
	// https://github.com/bwmarrin/discordgo
	// https://discord.com/developers/docs/topics/
	// voice-connections#encrypting-and-sending-voice
	var seq uint16 = 0
	var tstamp uint32 = 0
	headerBuf := [pageHeaderLen]byte{}
	segTable := [maxSegTableLen]byte{}
	packetBuf := [maxPacketLen]byte{}
	nonce := [nonceLen]byte{}
	header := [rtpHeaderLen]byte{}
	header[0] = 0x80
	header[1] = 0x78
	binary.BigEndian.PutUint32(header[8:], voiceGw.ssrc)
	// Define play
	play := func(song songFile) (int64, error) {
		var min int64 = time.Now().Unix()
		var max, avg, samps int64
		defer fmt.Print("\n")
		pLen := 0
		pNum := 0
		pStart := 0
		discard := 0
		// Open file
		f, err := os.Open(song.path)
		if err != nil {
			return song.offset, err
		}
		defer f.Close()
		if song.offset > 0 {
			_, err := f.Seek(song.offset, io.SeekCurrent)
			if err != nil {
				return song.offset, err
			}
		}
		prevt := time.Now()
		for {
			pageLen := 0
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
				return song.offset, err
			}
			pageLen += pageHeaderLen + tableLen
			if discard < 2 {
				sum := 0
				for _, v := range segTable[:tableLen] {
					sum += int(v)
				}
				_, err := f.Seek(int64(sum), io.SeekCurrent)
				if err != nil {
					return song.offset, err
				}
				discard += 1
				pageLen += sum
				song.offset += int64(pageLen)
				continue
			}
			for i := 0; i < tableLen; i++ {
				segLen := int(segTable[i])
				_, err = io.ReadFull(f, packetBuf[pStart:pStart+segLen])
				if err != nil {
					return song.offset, err
				}
				pageLen += segLen
				pLen += segLen
				if segLen == partialPacketLen {
					pStart += partialPacketLen
				} else {
					// more xsalsa20_poly1305 stuff
					binary.BigEndian.PutUint16(header[2:], seq)
					binary.BigEndian.PutUint32(header[4:], tstamp)
					copy(nonce[:], header[:])
					encrypted := secretbox.Seal(
						header[:],
						packetBuf[:pLen],
						&nonce,
						&voiceGw.secretKey,
					)
					_, err := sock.Write(encrypted)
					if err != nil {
						return song.offset, err
					}
					seq += 1
					tstamp += 960
					pLen = 0
					pStart = 0
					pNum += 1
					if pNum == packetBurst {
						dt := burstDuration - time.Since(prevt)
						dt64 := dt.Microseconds()
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
						if dt > 0 {
							time.Sleep(dt)
						}
						pNum = 0
						prevt = time.Now()
					}
					select {
					case song := <-voiceGw.songCtrl:
						if song.offset == songNormalStop {
							return songNormalStop, nil
						}
					case <-ctx.Done():
						return song.offset, ctx.Err()
					default:
						// Do nothing
					}
				}
			}
			song.offset += int64(pageLen)
		}
		return songNormalStop, nil
	}
	for {
		select {
		case song := <-voiceGw.songCtrl:
			if song.offset == songNormalStop {
				// Do nothing
				break
			}
			err := vSend(voiceGw.ws, ctx, &speakingPayload)
			if err != nil {
				log(err)
			}
			offset, err := play(song)
			if err != nil {
				log(err)
			}
			err = vSend(voiceGw.ws, ctx, &silentPayload)
			if err != nil {
				log(err)
			}
			song.offset = offset
			voiceGw.songDone <- song
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
		slog.Debug(
			"voice read",
			"op", voiceOpcodeNames[payload.Op],
		)
	}
	return err
}

func vSend(c *websocket.Conn, ctx context.Context, payload *voiceGwPayload) error {
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
