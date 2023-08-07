package internal

import (
	"encoding/json"

	"nhooyr.io/websocket"
)

const GATEWAY_INTENTS = 1<<7 | 1<<20 | 1<<21

var GATEWAY_PROPERTIES = identifyProperties{
	Os:      "linux",
	Browser: "disco",
	Device:  "lenovo thinkcentre",
}

type opcode int8

// https://discord.com/developers/docs/topics/opcodes-and-status-codes
const (
	OPC_DISPATCH opcode = iota
	OPC_HEARTBEAT
	OPC_IDENTIFY
	OPC_PRESENCE_UPDATE
	OPC_VOICE_STATE_UPDATE
	_
	OPC_RESUME
	OPC_RECONNECT
	OPC_REQUEST_GUILD_MEM
	OPC_INVALID_SESSION
	OPC_HELLO
	OPC_HEARTBEAT_ACK
)

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
