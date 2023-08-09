package internal

import (
	"encoding/json"

	"nhooyr.io/websocket"
)

type opcode int8

// https://discord.com/developers/docs/topics/opcodes-and-status-codes
const (
	Dispatch opcode = iota
	Heartbeat
	Identify
	PresenceUpdate
	VoiceStateUpdate
	_
	Resume
	Reconnect
	RequestGuildMem
	InvalidSession
	Hello
	HeartbeatAck
)

var OpcodeNames = map[opcode]string{
	Dispatch:         "DISPATCH",
	Heartbeat:        "HEARTBEAT",
	Identify:         "IDENTIFY",
	PresenceUpdate:   "PRESENCE_UPDATE",
	VoiceStateUpdate: "VOICE_STATE_UPDATE",
	Resume:           "RESUME",
	Reconnect:        "RECONNECT",
	RequestGuildMem:  "REQUEST_GUILD_MEM",
	InvalidSession:   "INVALID_SESSION",
	Hello:            "HELLO",
	HeartbeatAck:     "HEARTBEAT_ACK",
}

const (
	StatusGatewayUnknownErr websocket.StatusCode = iota + 4000
	StatusGatewayUnknownOp
	StatusGatewayDecodeErr
	StatusGatewayNotAuthd
	StatusGatewayAuthFailed
	StatusGatewayAlreadyAuthd
	_
	StatusGatewayInvldSeq
	StatusGatewayRateLimited
	StatusGatewaySessTimeout
	StatusGatewayInvldShard
	StatusGatewayShardRequired
	StatusGatewayInvldVers
	StatusGatewayInvldIntents
	StatusGatewayDisallowedIntents
	StatusGatewayInvalidSession websocket.StatusCode = 4024
)

var ValidResumeCodes = map[websocket.StatusCode]bool{
	StatusGatewayUnknownErr:        true,
	StatusGatewayUnknownOp:         true,
	StatusGatewayDecodeErr:         true,
	StatusGatewayNotAuthd:          true,
	StatusGatewayAuthFailed:        false,
	StatusGatewayAlreadyAuthd:      true,
	StatusGatewayInvldSeq:          true,
	StatusGatewayRateLimited:       true,
	StatusGatewaySessTimeout:       true,
	StatusGatewayInvldShard:        false,
	StatusGatewayShardRequired:     false,
	StatusGatewayInvldVers:         false,
	StatusGatewayInvldIntents:      false,
	StatusGatewayDisallowedIntents: false,
	// Custom
	websocket.StatusServiceRestart: true,
	StatusGatewayInvalidSession:    false,
	websocket.StatusNormalClosure:  false,
	websocket.StatusGoingAway:      false,
}

type gatewayRead struct {
	Op opcode          `json:"op"`
	D  json.RawMessage `json:"d"`
	S  int64           `json:"s"`
	T  string          `json:"t"`
}

type gatewaySend struct {
	Op opcode          `json:"op"`
	D  json.RawMessage `json:"d"`
}

type helloData struct {
	Interval int64           `json:"heartbeat_interval"`
	Trace    json.RawMessage `json:"_trace"`
}

type identifyData struct {
	Token      string             `json:"token"`
	Intents    int64              `json:"intents"`
	Properties identifyProperties `json:"properties"`
}
type identifyProperties struct {
	Os      string `json:"os"`
	Browser string `json:"browser"`
	Device  string `json:"device"`
}

type readyData struct {
	V         int64  `json:"v"`
	SessionId string `json:"session_id"`
	ResumeUrl string `json:"resume_gateway_url"`
}

type resumeData struct {
	Token     string `json:"token"`
	SessionId string `json:"session_id"`
	S         int64  `json:"seq"`
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
}
type InteractionMember struct {
	User InteractionUser `json:"user"`
}
type InteractionUser struct {
	Id string `json:"id"`
}
type InteractionSubData struct {
	Type    int64               `json:"type"`
	Name    string              `json:"name"`
	Options []InteractionOption `json:"options"`
}
type InteractionOption struct {
	Value string `json:"value"`
}
