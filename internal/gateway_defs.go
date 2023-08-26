package internal

import (
	"encoding/json"

	"nhooyr.io/websocket"
)

type opcode int

// https://discord.com/developers/docs/topics/opcodes-and-status-codes
const (
	dispatch opcode = iota
	heartbeat
	identify
	presenceUpdate
	voiceStateUpdate
	_
	resume
	reconnect
	requestGuildMem
	invalidSession
	hello
	heartbeatAck
)

var opcodeNames = map[opcode]string{
	dispatch:         "DISPATCH",
	heartbeat:        "HEARTBEAT",
	identify:         "IDENTIFY",
	presenceUpdate:   "PRESENCE_UPDATE",
	voiceStateUpdate: "VOICE_STATE_UPDATE",
	resume:           "RESUME",
	reconnect:        "RECONNECT",
	requestGuildMem:  "REQUEST_GUILD_MEM",
	invalidSession:   "INVALID_SESSION",
	hello:            "HELLO",
	heartbeatAck:     "HEARTBEAT_ACK",
}

const (
	statusGatewayUnknownErr websocket.StatusCode = iota + 4000
	statusGatewayUnknownOp
	statusGatewayDecodeErr
	statusGatewayNotAuthd
	statusGatewayAuthFailed
	statusGatewayAlreadyAuthd
	_
	statusGatewayInvldSeq
	statusGatewayRateLimited
	statusGatewaySessTimeout
	statusGatewayInvldShard
	statusGatewayShardRequired
	statusGatewayInvldVers
	statusGatewayInvldIntents
	statusGatewayDisallowedIntents
	statusGatewayInvalidSession websocket.StatusCode = 4024
)

var validResumeCodes = map[websocket.StatusCode]bool{
	statusGatewayUnknownErr:        true,
	statusGatewayUnknownOp:         true,
	statusGatewayDecodeErr:         true,
	statusGatewayNotAuthd:          true,
	statusGatewayAuthFailed:        false,
	statusGatewayAlreadyAuthd:      true,
	statusGatewayInvldSeq:          true,
	statusGatewayRateLimited:       true,
	statusGatewaySessTimeout:       true,
	statusGatewayInvldShard:        false,
	statusGatewayShardRequired:     false,
	statusGatewayInvldVers:         false,
	statusGatewayInvldIntents:      false,
	statusGatewayDisallowedIntents: false,
	// Custom
	websocket.StatusServiceRestart: true,
	statusGatewayInvalidSession:    false,
	websocket.StatusNormalClosure:  false,
	websocket.StatusGoingAway:      false,
}

type gatewayRead struct {
	Op opcode          `json:"op"`
	D  json.RawMessage `json:"d"`
	S  int             `json:"s"`
	T  string          `json:"t"`
}

type gatewaySend struct {
	Op opcode          `json:"op"`
	D  json.RawMessage `json:"d"`
}

type helloData struct {
	Interval int             `json:"heartbeat_interval"`
	Trace    json.RawMessage `json:"_trace"`
}

type identifyData struct {
	Token      string             `json:"token"`
	Intents    int                `json:"intents"`
	Properties identifyProperties `json:"properties"`
}
type identifyProperties struct {
	Os      string `json:"os"`
	Browser string `json:"browser"`
	Device  string `json:"device"`
}

type readyData struct {
	V         int    `json:"v"`
	SessionId string `json:"session_id"`
	ResumeUrl string `json:"resume_gateway_url"`
}

type resumeData struct {
	Token     string `json:"token"`
	SessionId string `json:"session_id"`
	S         int    `json:"seq"`
}

type voiceStateData struct {
	GuildId   string `json:"guild_id"`
	ChannelId string `json:"channel_id"`
	UserId    string `json:"user_id"`
	SessionId string `json:"session_id"`
}

type voiceStateUpdateData struct {
	GuildId   string  `json:"guild_id"`
	ChannelId *string `json:"channel_id"`
	SelfMute  bool    `json:"self_mute"`
	SelfDeaf  bool    `json:"self_deaf"`
}

type voiceServerUpdateData struct {
	GuildId  string `json:"guild_id"`
	Token    string `json:"token"`
	Endpoint string `json:"endpoint"`
}

type InteractionData struct {
	Token   string             `json:"token"`
	Member  InteractionMember  `json:"member"`
	Id      string             `json:"id"`
	GuildId string             `json:"guild_id"`
	Data    InteractionSubData `json:"data"`
	ChnlId  string             `json:"-"`
}
type InteractionMember struct {
	User InteractionUser `json:"user"`
}
type InteractionUser struct {
	Id string `json:"id"`
}
type InteractionSubData struct {
	Type    int                 `json:"type"`
	Name    string              `json:"name"`
	Options []InteractionOption `json:"options"`
}
type InteractionOption struct {
	Value string `json:"value"`
}

type voiceopcode int

const (
	voiceIdentify voiceopcode = iota
	voiceSelectPrtcl
	voiceReady
	voiceHeartbeat
	voiceSessDesc
	voiceSpeaking
	voiceHeartbeatAck
	voiceResume
	voiceHello
	voiceResumed
	voiceClientDiscon voiceopcode = 13
)

var voiceOpcodeNames = map[voiceopcode]string{
	voiceIdentify:     "IDENTIFY",
	voiceSelectPrtcl:  "SELECT_PROTOCOL",
	voiceReady:        "READY",
	voiceHeartbeat:    "HEARTBEAT",
	voiceSessDesc:     "SESSION_DESCRIPTION",
	voiceSpeaking:     "SPEAKING",
	voiceHeartbeatAck: "HEARTBEAT_ACK",
	voiceResume:       "RESUME",
	voiceHello:        "HELLO",
	voiceResumed:      "RESUMED",
	voiceClientDiscon: "CLIENT_DISCONNECT",
}

const (
	statusVoiceGwUnknownOp websocket.StatusCode = iota + 4001
	statusVoiceGwDecodeErr
	statusVoiceGwNotAuthd
	statusVoiceGwAuthFailed
	statusVoiceGwAlreadyAuthd
	statusVoiceGwInvldSess
	_
	_
	statusVoiceGwSessTimeout
	_
	statusVoiceGwSrvNotFound
	statusVoiceGwUnknownPrtcl
	_
	statusVoiceGwDisconnect
	statusVoiceGwServerCrash
	statusVoiceGwUnknownEncy
)

var voiceValidResumeCodes = map[websocket.StatusCode]bool{
	statusVoiceGwUnknownOp:    true,
	statusVoiceGwDecodeErr:    true,
	statusVoiceGwNotAuthd:     true,
	statusVoiceGwAuthFailed:   false,
	statusVoiceGwAlreadyAuthd: true,
	statusVoiceGwInvldSess:    false,
	statusVoiceGwSessTimeout:  false,
	statusVoiceGwSrvNotFound:  false,
	statusVoiceGwUnknownPrtcl: true,
	statusVoiceGwDisconnect:   false,
	statusVoiceGwServerCrash:  true,
	statusVoiceGwUnknownEncy:  true,
	// Custom
	websocket.StatusNormalClosure:  false,
	websocket.StatusServiceRestart: true,
}

type voiceGwPayload struct {
	Op voiceopcode     `json:"op"`
	D  json.RawMessage `json:"d"`
}

var cachedHeartbeat = voiceGwPayload{
	Op: voiceHeartbeat,
	// Voice nonce = 123403290
	D: []byte{49, 50, 51, 52, 48, 51, 50, 57, 48},
}

type voiceHelloData struct {
	Interval float64 `json:"heartbeat_interval"`
}

type voiceIdentifyData struct {
	ServerId  string `json:"server_id"`
	UserId    string `json:"user_id"`
	SessionId string `json:"session_id"`
	Token     string `json:"token"`
}

type voiceReadyData struct {
	Ssrc uint32 `json:"ssrc"`
	Ip   string `json:"ip"`
	Port int    `json:"port"`
}

type voiceSelectPrtclData struct {
	Protocol string                  `json:"protocol"`
	Data     voiceSelectPrtclSubData `json:"data"`
}

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

type voiceSelectPrtclSubData struct {
	Addr string `json:"address"`
	Port int    `json:"port"`
	Mode string `json:"mode"`
}

type voiceSessionDesc struct {
	Mode      string          `json:"mode"`
	SecretKey json.RawMessage `json:"secret_key"`
}

type voiceResumeData struct {
	ServerId  string `json:"server_id"`
	SessionId string `json:"session_id"`
	Token     string `json:"token"`
}

type voiceSpeakingData struct {
	Speaking int    `json:"speaking"`
	Delay    int    `json:"delay"`
	Ssrc     uint32 `json:"ssrc"`
}
