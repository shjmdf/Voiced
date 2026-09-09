package signaling

import (
	"context"
	"sync"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Client represents one browser's currently open WebSocket connection.
// room.Manager decides the content, client decides the delivery.
type Client struct {
	roomID        string
	participantID string
	connection    *websocket.Conn

	send      chan Message
	closeOnce sync.Once
}

func NewClient(roomID string, participantID string, connection *websocket.Conn) *Client {
	return &Client{
		roomID:        roomID,
		participantID: participantID,
		connection:    connection,
		send:          make(chan Message, 32),
	}
}

func (c *Client) RoomID() string {
	return c.roomID
}

func (c *Client) ParticipantID() string {
	return c.participantID
}

// Send places a server event in this browser's queue. false means the
// browser is too slow to receive more events right now.
func (c *Client) Send(message Message) bool {
	select {
	case c.send <- message:
		return true
	default:
		return false
	}
}

// WriteLoop is run in its own goroutine. It is the only code path that writes
// WebSocket messages for this client, which preserves their order.
func (c *Client) WriteLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case message := <-c.send:
			if err := wsjson.Write(ctx, c.connection, message); err != nil {
				return err
			}
		}
	}
}

func (c *Client) Close() {
	c.CloseWithStatus(websocket.StatusNormalClosure, "")
}

func (c *Client) CloseWithStatus(status websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		_ = c.connection.Close(status, reason)
	})
}
