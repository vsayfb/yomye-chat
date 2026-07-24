package hub

import (
	"sync"

	"github.com/gorilla/websocket"
)

type Client struct {
	UserID     string
	ReceiverID string
	Conn       *websocket.Conn
}

type Hub struct {
	mu          sync.RWMutex
	connections map[string]*Client // userID → Client
}

func New() *Hub {
	return &Hub{
		connections: make(map[string]*Client),
	}
}

func (h *Hub) Register(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connections[client.UserID] = client
}

func (h *Hub) Unregister(userID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.connections, userID)
}

// Returns false if the user is not connected.
func (h *Hub) Send(userID string, msg []byte) bool {
	h.mu.RLock()

	client, ok := h.connections[userID]

	h.mu.RUnlock()

	if !ok {
		return false
	}

	if err := client.Conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		h.Unregister(userID)
		return false
	}

	return true
}

func (h *Hub) IsConnected(userID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.connections[userID]
	return ok
}
