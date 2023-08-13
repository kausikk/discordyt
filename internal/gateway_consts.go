package internal

import (
	"encoding/json"

	"nhooyr.io/websocket"
)

type opcode int8

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
