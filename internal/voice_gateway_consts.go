package internal

import (
	"encoding/json"

	"nhooyr.io/websocket"
)

type voiceopcode int8

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
	Speaking int64  `json:"speaking"`
	Delay    int64  `json:"delay"`
	Ssrc     uint32 `json:"ssrc"`
}
