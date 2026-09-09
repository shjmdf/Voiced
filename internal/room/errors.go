package room

import "errors"

var (
	ErrNotFound            = errors.New("room not found")
	ErrRoomNameRequired    = errors.New("room name is required")
	ErrNicknameRequired    = errors.New("nickname is required")
	ErrNotOwner            = errors.New("only the room owner may do this")
	ErrParticipantNotFound = errors.New("participant not found")
	ErrCannotRemoveOwner   = errors.New("the owner must leave instead")
)
