package signaling

import "sync"

// Hub keeps track of currently connected browsers, grouped by room.
type Hub struct {
	mu    sync.RWMutex
	rooms map[string]map[string]*Client
}

func NewHub() *Hub {
	return &Hub{
		rooms: make(map[string]map[string]*Client),
	}
}

// Add registers a connected browser. If this participant reconnects, Add
// returns its earlier connection so the caller can close it.
func (h *Hub) Add(client *Client) (previous *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients, exists := h.rooms[client.RoomID()]
	if !exists {
		clients = make(map[string]*Client)
		h.rooms[client.RoomID()] = clients
	}

	previous = clients[client.ParticipantID()]
	clients[client.ParticipantID()] = client
	return previous
}

// Remove unregisters this exact connection. The pointer comparison prevents
// an old connection from removing a newer reconnection for the same user.
func (h *Hub) Remove(client *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients, exists := h.rooms[client.RoomID()]
	if !exists || clients[client.ParticipantID()] != client {
		return false
	}

	delete(clients, client.ParticipantID())
	if len(clients) == 0 {
		delete(h.rooms, client.RoomID())
	}

	return true
}

// Broadcast queues a message for every currently connected browser in roomID.
func (h *Hub) Broadcast(roomID string, message Message) {
	clients := h.clientsInRoom(roomID)
	for _, client := range clients {
		if client.Send(message) {
			continue
		}

		// A full queue means the browser is no longer keeping up. Disconnect it
		// instead of retaining unbounded messages in server memory.
		client.Close()
	}
}

// SendTo queues a message for one connected browser. WebRTC answers and ICE
// candidates use this path because they belong to one peer connection.
func (h *Hub) SendTo(roomID string, participantID string, message Message) bool {
	h.mu.RLock()
	client := h.rooms[roomID][participantID]
	h.mu.RUnlock()
	if client == nil {
		return false
	}
	if client.Send(message) {
		return true
	}
	client.Close()
	return false
}

// Take removes one connection and returns it. It is used after room.Manager
// has already removed that participant from the room.
func (h *Hub) Take(roomID string, participantID string) *Client {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients, exists := h.rooms[roomID]
	if !exists {
		return nil
	}

	client := clients[participantID]
	delete(clients, participantID)
	if len(clients) == 0 {
		delete(h.rooms, roomID)
	}

	return client
}

// TakeRoom removes every connection in one room. It is used after the room
// has been dissolved in room.Manager.
func (h *Hub) TakeRoom(roomID string) []*Client {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients, exists := h.rooms[roomID]
	if !exists {
		return nil
	}

	result := make([]*Client, 0, len(clients))
	for _, client := range clients {
		result = append(result, client)
	}
	delete(h.rooms, roomID)
	return result
}

func (h *Hub) clientsInRoom(roomID string) []*Client {
	h.mu.RLock()
	defer h.mu.RUnlock()

	clients := h.rooms[roomID]
	result := make([]*Client, 0, len(clients))
	for _, client := range clients {
		result = append(result, client)
	}

	return result
}
