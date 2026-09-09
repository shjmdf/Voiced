package participant

import "time"

// Participant is one anonymous user's current membership in a room.
type Participant struct {
	ID           string    `json:"id"`
	Nickname     string    `json:"nickname"`
	JoinedAt     time.Time `json:"joinedAt"`
	MutedByOwner bool      `json:"mutedByOwner"`
}
