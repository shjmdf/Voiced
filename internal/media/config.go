// Package media owns the WebRTC peer connections and forwards audio RTP
// packets between participants in the same room.
package media

import (
	"errors"
	"fmt"

	"github.com/pion/webrtc/v4"
)

// Config is fixed when the server starts. ICE allocates its UDP sockets while
// creating peer connections, so changing this range after the server is
// already handling calls would not move existing connections to new ports.
type Config struct {
	UDPPortMin uint16
	UDPPortMax uint16
	ICEServers []webrtc.ICEServer
}

func (c Config) validate() error {
	if c.UDPPortMin == 0 && c.UDPPortMax == 0 {
		return nil
	}
	if c.UDPPortMin == 0 || c.UDPPortMax == 0 {
		return errors.New("both UDP port limits must be set")
	}
	if c.UDPPortMin > c.UDPPortMax {
		return fmt.Errorf("UDP port minimum %d is greater than maximum %d", c.UDPPortMin, c.UDPPortMax)
	}
	return nil
}

// PublicConfig is safe to return to a browser. It contains STUN/TURN server
// URLs but never TURN usernames or credentials.
type PublicConfig struct {
	ICEServers []PublicICEServer `json:"iceServers"`
}

type PublicICEServer struct {
	URLs []string `json:"urls"`
}

// DebugInfo describes the media server's current process state. It is meant
// for a local development endpoint, not for changing a running configuration.
type DebugInfo struct {
	UDPPortMin       uint16 `json:"udpPortMin"`
	UDPPortMax       uint16 `json:"udpPortMax"`
	PeerCount        int    `json:"peerCount"`
	PublicationCount int    `json:"publicationCount"`
}

// Signal is delivered through the existing WebSocket signaling layer.
type Signal struct {
	RoomID        string
	ParticipantID string
	Type          string
	Payload       any
}

type SignalSink func(Signal)

// MutedSource lets the room domain remain the authority for owner muting.
// It is consulted when a participant starts publishing after already being
// muted by the room owner.
type MutedSource func(roomID string, participantID string) bool
