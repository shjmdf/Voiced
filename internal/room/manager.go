package room

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"voiced/internal/participant"
)

// Manager owns every currently active room. One lock is deliberately enough
// for this first small service and keeps the state model easy to follow.
type Manager struct {
	mu    sync.RWMutex
	rooms map[string]*Room
}

func NewManager() *Manager {
	return &Manager{rooms: make(map[string]*Room)}
}

func (m *Manager) Create(roomName string, nickname string) (JoinResult, error) {
	roomName = strings.TrimSpace(roomName)
	if roomName == "" {
		return JoinResult{}, ErrRoomNameRequired
	}

	member, err := newParticipant(nickname)
	if err != nil {
		return JoinResult{}, err
	}

	roomID, err := newID()
	if err != nil {
		return JoinResult{}, err
	}

	room := &Room{
		ID:                 roomID,
		Name:               roomName,
		OwnerParticipantID: member.ID,
		CreatedAt:          time.Now().UTC(),
		participants:       map[string]participant.Participant{member.ID: member},
		participantOrder:   []string{member.ID},
	}

	m.mu.Lock()
	m.rooms[room.ID] = room
	m.mu.Unlock()

	return JoinResult{Room: room.info(), Participant: member}, nil
}

func (m *Manager) Join(roomID string, nickname string) (JoinResult, error) {
	member, err := newParticipant(nickname)
	if err != nil {
		return JoinResult{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	room, exists := m.rooms[roomID]
	if !exists {
		return JoinResult{}, ErrNotFound
	}

	room.participants[member.ID] = member
	room.participantOrder = append(room.participantOrder, member.ID)
	return JoinResult{Room: room.info(), Participant: member}, nil
}

func (m *Manager) Get(roomID string) (Info, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	room, exists := m.rooms[roomID]
	if !exists {
		return Info{}, ErrNotFound
	}

	return room.info(), nil
}

func (m *Manager) List() []Info {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rooms := make([]Info, 0, len(m.rooms))
	for _, room := range m.rooms {
		rooms = append(rooms, room.info())
	}

	return rooms
}

// Leave removes a member. If the owner leaves, ownership moves to the member
// who has been in the room the longest. The last leave deletes the room.
func (m *Manager) Leave(roomID string, participantID string) (LeaveResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	room, exists := m.rooms[roomID]
	if !exists {
		return LeaveResult{}, ErrNotFound
	}
	if _, exists := room.participants[participantID]; !exists {
		return LeaveResult{}, ErrParticipantNotFound
	}

	delete(room.participants, participantID)
	room.removeFromOrder(participantID)

	if len(room.participants) == 0 {
		delete(m.rooms, roomID)
		return LeaveResult{RoomDeleted: true}, nil
	}

	if room.OwnerParticipantID == participantID {
		room.OwnerParticipantID = room.participantOrder[0]
		return LeaveResult{NewOwnerParticipantID: room.OwnerParticipantID}, nil
	}

	return LeaveResult{}, nil
}

func (m *Manager) ListParticipants(roomID string) ([]participant.Participant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	room, exists := m.rooms[roomID]
	if !exists {
		return nil, ErrNotFound
	}

	members := make([]participant.Participant, 0, len(room.participantOrder))
	for _, participantID := range room.participantOrder {
		members = append(members, room.participants[participantID])
	}

	return members, nil
}

// GetParticipant verifies that participantID currently belongs to roomID.
func (m *Manager) GetParticipant(roomID string, participantID string) (participant.Participant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	room, exists := m.rooms[roomID]
	if !exists {
		return participant.Participant{}, ErrNotFound
	}

	member, exists := room.participants[participantID]
	if !exists {
		return participant.Participant{}, ErrParticipantNotFound
	}

	return member, nil
}

func (m *Manager) RemoveParticipant(roomID string, ownerParticipantID string, targetParticipantID string) (participant.Participant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	room, err := m.ownerRoom(roomID, ownerParticipantID)
	if err != nil {
		return participant.Participant{}, err
	}
	if targetParticipantID == room.OwnerParticipantID {
		return participant.Participant{}, ErrCannotRemoveOwner
	}

	member, exists := room.participants[targetParticipantID]
	if !exists {
		return participant.Participant{}, ErrParticipantNotFound
	}

	delete(room.participants, targetParticipantID)
	room.removeFromOrder(targetParticipantID)
	return member, nil
}

func (m *Manager) SetMuted(roomID string, ownerParticipantID string, targetParticipantID string, muted bool) (participant.Participant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	room, err := m.ownerRoom(roomID, ownerParticipantID)
	if err != nil {
		return participant.Participant{}, err
	}

	member, exists := room.participants[targetParticipantID]
	if !exists {
		return participant.Participant{}, ErrParticipantNotFound
	}

	member.MutedByOwner = muted
	room.participants[targetParticipantID] = member
	return member, nil
}

// Dissolve lets the current owner explicitly close the room for everybody.
func (m *Manager) Dissolve(roomID string, ownerParticipantID string) ([]participant.Participant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	room, err := m.ownerRoom(roomID, ownerParticipantID)
	if err != nil {
		return nil, err
	}

	members := room.participantsInJoinOrder()
	delete(m.rooms, roomID)
	return members, nil
}

func (m *Manager) ownerRoom(roomID string, ownerParticipantID string) (*Room, error) {
	room, exists := m.rooms[roomID]
	if !exists {
		return nil, ErrNotFound
	}
	if ownerParticipantID != room.OwnerParticipantID {
		return nil, ErrNotOwner
	}

	return room, nil
}

func (r *Room) info() Info {
	return Info{
		ID:                 r.ID,
		Name:               r.Name,
		OwnerParticipantID: r.OwnerParticipantID,
		CreatedAt:          r.CreatedAt,
		ParticipantCount:   len(r.participants),
	}
}

func (r *Room) removeFromOrder(participantID string) {
	for index, id := range r.participantOrder {
		if id == participantID {
			r.participantOrder = append(r.participantOrder[:index], r.participantOrder[index+1:]...)
			return
		}
	}
}

func (r *Room) participantsInJoinOrder() []participant.Participant {
	members := make([]participant.Participant, 0, len(r.participantOrder))
	for _, participantID := range r.participantOrder {
		members = append(members, r.participants[participantID])
	}

	return members
}

func newParticipant(nickname string) (participant.Participant, error) {
	nickname = strings.TrimSpace(nickname)
	if nickname == "" {
		return participant.Participant{}, ErrNicknameRequired
	}

	id, err := newID()
	if err != nil {
		return participant.Participant{}, err
	}

	return participant.Participant{
		ID:       id,
		Nickname: nickname,
		JoinedAt: time.Now().UTC(),
	}, nil
}

func newID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}

	return hex.EncodeToString(bytes[:]), nil
}
