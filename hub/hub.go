package hub

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Client struct {
	UserID     string
	ReceiverID string
	Conn       *websocket.Conn
	writeMu    sync.Mutex
}

func (c *Client) Send(msg []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.Conn.WriteMessage(websocket.TextMessage, msg)
}

func (c *Client) Close(code int, reason string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	deadline := time.Now().Add(5 * time.Second)
	return c.Conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason),
		deadline,
	)
}

type Hub struct {
	mu          sync.RWMutex
	connections map[string]map[*Client]struct{} // userID → active devices/tabs
}

func New() *Hub {
	return &Hub{
		connections: make(map[string]map[*Client]struct{}),
	}
}

func (h *Hub) Register(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.connections[client.UserID] == nil {
		h.connections[client.UserID] = make(map[*Client]struct{})
	}
	h.connections[client.UserID][client] = struct{}{}
}

func (h *Hub) Unregister(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	clients := h.connections[client.UserID]
	delete(clients, client)
	if len(clients) == 0 {
		delete(h.connections, client.UserID)
	}
}

// Send writes to every active connection for the user and reports whether at
// least one delivery succeeded.
func (h *Hub) Send(userID string, msg []byte) bool {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.connections[userID]))
	for client := range h.connections[userID] {
		clients = append(clients, client)
	}
	h.mu.RUnlock()

	delivered := false
	for _, client := range clients {
		if err := client.Send(msg); err != nil {
			h.Unregister(client)
			continue
		}
		delivered = true
	}
	return delivered
}

func (h *Hub) IsConnected(userID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.connections[userID]) > 0
}
