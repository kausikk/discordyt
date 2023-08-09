package internal

import (
	"encoding/json"

	"nhooyr.io/websocket"
)

type voiceopcode int8

const (
	VoiceIdentify voiceopcode = iota
	VoiceSelectPrtcl
	VoiceReady
	VoiceHeartbeat
	VoiceSessDesc
	VoiceSpeaking
	VoiceHeartbeatAck
	VoiceResume
	VoiceHello
	VoiceResumed
	VoiceClientDiscon voiceopcode = 13
)

var VoiceOpcodeNames = map[voiceopcode]string{
	VoiceIdentify:     "IDENTIFY",
	VoiceSelectPrtcl:  "SELECT_PROTOCOL",
	VoiceReady:        "READY",
	VoiceHeartbeat:    "HEARTBEAT",
	VoiceSessDesc:     "SESSION_DESCRIPTION",
	VoiceSpeaking:     "SPEAKING",
	VoiceHeartbeatAck: "HEARTBEAT_ACK",
	VoiceResume:       "RESUME",
	VoiceHello:        "HELLO",
	VoiceResumed:      "RESUMED",
	VoiceClientDiscon: "CLIENT_DISCONNECT",
}

const (
	StatusVoiceGwUnknownOp websocket.StatusCode = iota + 4001
	StatusVoiceGwDecodeErr
	StatusVoiceGwNotAuthd
	StatusVoiceGwAuthFailed
	StatusVoiceGwAlreadyAuthd
	StatusVoiceGwInvldSess
	_
	_
	StatusVoiceGwSessTimeout
	_
	StatusVoiceGwSrvNotFound
	StatusVoiceGwUnknownPrtcl
	_
	StatusVoiceGwDisconnect
	StatusVoiceGwServerCrash
	StatusVoiceGwUnknownEncy
)

var VoiceValidResumeCodes = map[websocket.StatusCode]bool{
	StatusVoiceGwUnknownOp:    true,
	StatusVoiceGwDecodeErr:    true,
	StatusVoiceGwNotAuthd:     true,
	StatusVoiceGwAuthFailed:   false,
	StatusVoiceGwAlreadyAuthd: true,
	StatusVoiceGwInvldSess:    false,
	StatusVoiceGwSessTimeout:  false,
	StatusVoiceGwSrvNotFound:  false,
	StatusVoiceGwUnknownPrtcl: true,
	StatusVoiceGwDisconnect:   false,
	StatusVoiceGwServerCrash:  true,
	StatusVoiceGwUnknownEncy:  true,
	// Custom
	websocket.StatusNormalClosure:  false,
	websocket.StatusServiceRestart: true,
}

type voiceGwPayload struct {
	Op voiceopcode     `json:"op"`
	D  json.RawMessage `json:"d"`
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
	Ssrc int32  `json:"ssrc"`
	Ip   string `json:"ip"`
	Port int64  `json:"port"`
}

type voiceSelectPrtclData struct {
	Protocol string                  `json:"protocol"`
	Data     voiceSelectPrtclSubData `json:"data"`
}

type voiceSelectPrtclSubData struct {
	Addr string `json:"address"`
	Port int64  `json:"port"`
	Mode string `json:"mode"`
}

type voiceSessDesc struct {
	Mode      string `json:"mode"`
	SecretKey []byte `json:"secret_key"`
}

type voiceResumeData struct {
	ServerId  string `json:"server_id"`
	SessionId string `json:"session_id"`
	Token     string `json:"token"`
}
