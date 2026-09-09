package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/pion/webrtc/v4"

	"voiced/internal/media"
	"voiced/internal/participant"
	"voiced/internal/room"
)

type Handler struct {
	rooms          *room.Manager
	hub            *Hub
	media          *media.Manager
	originPatterns []string
}

func NewHandler(rooms *room.Manager, hub *Hub, mediaManager *media.Manager, originPatterns []string) *Handler {
	return &Handler{rooms: rooms, hub: hub, media: mediaManager, originPatterns: originPatterns}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomID")
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.originPatterns})
	if err != nil {
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer connection.Close(websocket.StatusNormalClosure, "")

	member, err := h.authenticate(ctx, connection, roomID)
	if err != nil {
		h.reject(ctx, connection, err)
		return
	}

	client := NewClient(roomID, member.ID, connection)
	if previous := h.hub.Add(client); previous != nil {
		previous.Close()
	}
	defer h.disconnect(client)

	go func() {
		if client.WriteLoop(ctx) != nil {
			cancel()
			client.Close()
		}
	}()

	if !h.sendSnapshot(client) {
		return
	}

	// Existing browsers can now show this participant immediately. A reconnect
	// produces the same event; the browser should update by participant ID.
	h.hub.Broadcast(roomID, newMessage("participant_connected", member))

	for {
		var message Message
		if err := wsjson.Read(ctx, connection, &message); err != nil {
			return
		}

		switch message.Type {
		case "ping":
			client.Send(newMessage("pong", nil))
		case "leave":
			return
		case "offer":
			h.handleOffer(client, message)
		case "answer":
			h.handleAnswer(client, message)
		case "ice_candidate":
			h.handleICECandidate(client, message)
		case "mute_participant":
			h.setMuted(client, message)
		case "remove_participant":
			h.removeParticipant(client, message)
		case "dissolve_room":
			h.dissolveRoom(client)
		default:
			client.Send(newMessage("error", errorPayload{
				Code:    "UNSUPPORTED_MESSAGE",
				Message: "this message type is not supported yet",
			}))
		}
	}
}

func (h *Handler) authenticate(ctx context.Context, connection *websocket.Conn, roomID string) (participant.Participant, error) {
	var message Message
	if err := wsjson.Read(ctx, connection, &message); err != nil {
		return participant.Participant{}, err
	}
	if message.Type != "authenticate" {
		return participant.Participant{}, errAuthenticationRequired
	}

	var payload struct {
		ParticipantID string `json:"participantId"`
	}
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		return participant.Participant{}, errAuthenticationRequired
	}

	member, err := h.rooms.GetParticipant(roomID, strings.TrimSpace(payload.ParticipantID))
	if err != nil {
		return participant.Participant{}, err
	}

	return member, nil
}

func (h *Handler) sendSnapshot(client *Client) bool {
	roomInfo, err := h.rooms.Get(client.RoomID())
	if err != nil {
		return false
	}

	members, err := h.rooms.ListParticipants(client.RoomID())
	if err != nil {
		return false
	}

	return client.Send(newMessage("room_snapshot", struct {
		Room         room.Info                 `json:"room"`
		Participants []participant.Participant `json:"participants"`
	}{
		Room:         roomInfo,
		Participants: members,
	}))
}

func (h *Handler) disconnect(client *Client) {
	// A reconnect replaces the old Client. Only the Client that Hub currently
	// holds is allowed to remove the participant from the room.
	if !h.hub.Remove(client) {
		return
	}
	h.media.ClosePeer(client.RoomID(), client.ParticipantID())

	result, err := h.rooms.Leave(client.RoomID(), client.ParticipantID())
	if err != nil {
		return
	}

	h.hub.Broadcast(client.RoomID(), newMessage("participant_left", participantPayload{
		ID: client.ParticipantID(),
	}))
	if result.NewOwnerParticipantID != "" {
		h.hub.Broadcast(client.RoomID(), newMessage("owner_changed", struct {
			OwnerParticipantID string `json:"ownerParticipantId"`
		}{
			OwnerParticipantID: result.NewOwnerParticipantID,
		}))
	}
}

func (h *Handler) setMuted(client *Client, message Message) {
	var payload struct {
		TargetParticipantID string `json:"targetParticipantId"`
		Muted               bool   `json:"muted"`
	}
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		h.sendControlError(client, "INVALID_MESSAGE", "mute_participant has an invalid payload")
		return
	}

	member, err := h.rooms.SetMuted(
		client.RoomID(),
		client.ParticipantID(),
		payload.TargetParticipantID,
		payload.Muted,
	)
	if err != nil {
		h.sendRoomError(client, err)
		return
	}

	h.hub.Broadcast(client.RoomID(), newMessage("participant_muted", member))
	h.media.SetMuted(client.RoomID(), member.ID, member.MutedByOwner)
}

func (h *Handler) removeParticipant(client *Client, message Message) {
	var payload struct {
		TargetParticipantID string `json:"targetParticipantId"`
	}
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		h.sendControlError(client, "INVALID_MESSAGE", "remove_participant has an invalid payload")
		return
	}

	member, err := h.rooms.RemoveParticipant(
		client.RoomID(),
		client.ParticipantID(),
		payload.TargetParticipantID,
	)
	if err != nil {
		h.sendRoomError(client, err)
		return
	}
	h.media.ClosePeer(client.RoomID(), member.ID)

	if removedClient := h.hub.Take(client.RoomID(), member.ID); removedClient != nil {
		removedClient.CloseWithStatus(websocket.StatusPolicyViolation, "removed by room owner")
	}
	h.hub.Broadcast(client.RoomID(), newMessage("participant_removed", member))
}

func (h *Handler) dissolveRoom(client *Client) {
	_, err := h.rooms.Dissolve(client.RoomID(), client.ParticipantID())
	if err != nil {
		h.sendRoomError(client, err)
		return
	}
	h.media.CloseRoom(client.RoomID())

	for _, roomClient := range h.hub.TakeRoom(client.RoomID()) {
		roomClient.CloseWithStatus(websocket.StatusPolicyViolation, "room dissolved")
	}
}

func (h *Handler) handleOffer(client *Client, message Message) {
	var offer webrtc.SessionDescription
	if err := json.Unmarshal(message.Payload, &offer); err != nil {
		h.sendControlError(client, "INVALID_MESSAGE", "offer has an invalid payload")
		return
	}

	answer, err := h.media.HandleOffer(client.RoomID(), client.ParticipantID(), offer)
	if err != nil {
		h.sendControlError(client, "WEBRTC_ERROR", err.Error())
		return
	}
	client.Send(newMessage("answer", answer))
}

func (h *Handler) handleAnswer(client *Client, message Message) {
	var answer webrtc.SessionDescription
	if err := json.Unmarshal(message.Payload, &answer); err != nil {
		h.sendControlError(client, "INVALID_MESSAGE", "answer has an invalid payload")
		return
	}
	if err := h.media.HandleAnswer(client.RoomID(), client.ParticipantID(), answer); err != nil {
		h.sendControlError(client, "WEBRTC_ERROR", err.Error())
	}
}

func (h *Handler) handleICECandidate(client *Client, message Message) {
	var candidate webrtc.ICECandidateInit
	if err := json.Unmarshal(message.Payload, &candidate); err != nil {
		h.sendControlError(client, "INVALID_MESSAGE", "ice_candidate has an invalid payload")
		return
	}
	if err := h.media.AddICECandidate(client.RoomID(), client.ParticipantID(), candidate); err != nil {
		h.sendControlError(client, "WEBRTC_ERROR", err.Error())
	}
}

func (h *Handler) sendRoomError(client *Client, err error) {
	switch {
	case errors.Is(err, room.ErrNotOwner):
		h.sendControlError(client, "NOT_OWNER", err.Error())
	case errors.Is(err, room.ErrNotFound), errors.Is(err, room.ErrParticipantNotFound):
		h.sendControlError(client, "NOT_FOUND", err.Error())
	case errors.Is(err, room.ErrCannotRemoveOwner):
		h.sendControlError(client, "OWNER_MUST_LEAVE", err.Error())
	default:
		h.sendControlError(client, "INVALID_REQUEST", err.Error())
	}
}

func (h *Handler) sendControlError(client *Client, code string, message string) {
	client.Send(newMessage("error", errorPayload{Code: code, Message: message}))
}

func (h *Handler) reject(ctx context.Context, connection *websocket.Conn, err error) {
	code := "AUTHENTICATION_FAILED"
	message := "authentication failed"
	if errors.Is(err, room.ErrNotFound) {
		code = "ROOM_NOT_FOUND"
		message = "room does not exist"
	}

	_ = wsjson.Write(ctx, connection, newMessage("error", errorPayload{
		Code:    code,
		Message: message,
	}))
	_ = connection.Close(websocket.StatusPolicyViolation, message)
}

type participantPayload struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname,omitempty"`
}

type errorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func newMessage(messageType string, payload any) Message {
	return NewMessage(messageType, payload)
}

var errAuthenticationRequired = errors.New("authenticate before sending other messages")
