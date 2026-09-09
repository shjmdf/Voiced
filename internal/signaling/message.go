package signaling

import "encoding/json"

type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// NewMessage marshals a typed payload once at the signaling boundary.
// Every WebSocket message in this project has the same envelope.
func NewMessage(messageType string, payload any) Message {
	encoded, err := json.Marshal(payload)
	if err != nil {
		// All payloads originate in our own server code. A JSON null is safer
		// than sending a malformed WebSocket message if a future payload fails.
		encoded = []byte("null")
	}
	return Message{Type: messageType, Payload: encoded}
}
