package protocol

import "encoding/json"

// Binary frame type prefixes
const (
	FrameOutput byte = 0x01 // daemon -> client: terminal output
	FrameInput  byte = 0x02 // client -> daemon: terminal input
	FrameResize byte = 0x03 // client -> daemon: resize (4 bytes: cols_hi, cols_lo, rows_hi, rows_lo)
)

// Control message types (JSON text frames)
const (
	TypeAuth         = "auth"
	TypeAuthOK       = "auth_ok"
	TypeAuthFailed   = "auth_failed"
	TypeListSessions = "list_sessions"
	TypeSessionList  = "session_list"
	TypeAttach       = "attach"
	TypeAttached     = "attached"
	TypeDetach       = "detach"
	TypeDetached     = "detached"
	TypeCreate       = "create"
	TypeKill         = "kill"
	TypeError        = "error"
	TypePing         = "ping"
	TypePong         = "pong"
)

// Message is the envelope for all JSON control messages.
type Message struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Auth messages
type AuthRequest struct {
	Token    string `json:"token"`
	ClientID string `json:"client_id"`
}

type AuthOKResponse struct {
	MachineName string `json:"machine_name"`
	Version     string `json:"version"`
}

// Session messages
type SessionInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"` // "pty" or "tmux"
	Running bool   `json:"running"`
	Created int64  `json:"created"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
}

type SessionListResponse struct {
	Sessions []SessionInfo `json:"sessions"`
}

type AttachRequest struct {
	SessionID string `json:"session_id"`
}

type AttachedResponse struct {
	SessionID string `json:"session_id"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

type CreateRequest struct {
	Name  string `json:"name,omitempty"`
	Shell string `json:"shell,omitempty"`
	Type  string `json:"type,omitempty"` // "auto", "pty", "tmux"
	Cols  int    `json:"cols,omitempty"`
	Rows  int    `json:"rows,omitempty"`
}

type KillRequest struct {
	SessionID string `json:"session_id"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Encode creates a Message from a typed payload.
func Encode(msgType string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Message{Type: msgType, Data: data})
}

// Decode parses a raw JSON message into its envelope.
func Decode(raw []byte) (*Message, error) {
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// DecodeData unmarshals the Data field into a typed struct.
func DecodeData[T any](msg *Message) (*T, error) {
	var t T
	if err := json.Unmarshal(msg.Data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// EncodeBinaryFrame creates a binary frame with a type prefix.
func EncodeBinaryFrame(frameType byte, data []byte) []byte {
	frame := make([]byte, 1+len(data))
	frame[0] = frameType
	copy(frame[1:], data)
	return frame
}

// EncodeResize creates a resize binary frame.
func EncodeResize(cols, rows uint16) []byte {
	return []byte{
		FrameResize,
		byte(cols >> 8), byte(cols & 0xFF),
		byte(rows >> 8), byte(rows & 0xFF),
	}
}

// DecodeResize extracts cols and rows from a resize frame's data (after type byte).
func DecodeResize(data []byte) (cols, rows uint16) {
	if len(data) < 4 {
		return 80, 24 // safe default
	}
	cols = uint16(data[0])<<8 | uint16(data[1])
	rows = uint16(data[2])<<8 | uint16(data[3])
	return
}
