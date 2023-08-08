package internal

import (
	"encoding/json"

	"nhooyr.io/websocket"
)

const VoiceNonce = 123403290

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

var ValidVoiceResumeCodes = map[websocket.StatusCode]bool{
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
	Ssrc int64  `json:"ssrc"`
	Ip   string `json:"ip"`
	Port int64  `json:"port"`
}

type voiceSelectPrtclData struct {
	Addr string `json:"address"`
	Port int64  `json:"port"`
	Mode string `json:"mode"`
}

type voiceSessDesc struct {
	Mode      string `json:"mode"`
	SecretKey []byte `json:"secret_key"`
}
