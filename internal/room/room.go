package room

import (
	"time"

	"voiced/internal/participant"
)

// Room is internal runtime state. It disappears after its last member leaves.
type Room struct {
	ID                 string
	Name               string
	OwnerParticipantID string
	CreatedAt          time.Time

	participants     map[string]participant.Participant
	participantOrder []string
}

// Info is the public, serializable room data returned to a browser.
type Info struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	OwnerParticipantID string    `json:"ownerParticipantId"`
	CreatedAt          time.Time `json:"createdAt"`
	ParticipantCount   int       `json:"participantCount"`
}

// JoinResult is returned after creating or joining a room. The browser keeps
// Participant.ID only for the current anonymous session.
type JoinResult struct {
	Room        Info                    `json:"room"`
	Participant participant.Participant `json:"participant"`
}

type LeaveResult struct {
	RoomDeleted           bool   `json:"roomDeleted"`
	NewOwnerParticipantID string `json:"newOwnerParticipantId,omitempty"`
}
