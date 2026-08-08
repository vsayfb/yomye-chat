package hub

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	sendQueueSize  = 64
	maxMessageSize = 64 << 10 // 64 KiB
)

var (
	ErrClientClosed  = errors.New("websocket client closed")
	ErrSendQueueFull = errors.New("websocket send queue full")
)

type Client struct {
	UserID     string
	ReceiverID string
	Channel    string
	Conn       *websocket.Conn

	send       chan []byte
	done       chan struct{}
	writerOnce sync.Once
	closeOnce  sync.Once
}

func NewClient(userID, receiverID, channel string, conn *websocket.Conn) *Client {
	return &Client{
		UserID:     userID,
		ReceiverID: receiverID,
		Channel:    channel,
		Conn:       conn,
		send:       make(chan []byte, sendQueueSize),
		done:       make(chan struct{}),
	}
}

func (c *Client) start() {
	c.writerOnce.Do(func() {
		c.Conn.SetReadLimit(maxMessageSize)
		_ = c.Conn.SetReadDeadline(time.Now().Add(pongWait))
		c.Conn.SetPongHandler(func(string) error {
			return c.Conn.SetReadDeadline(time.Now().Add(pongWait))
		})

		go c.writePump()
	})
}

// Send queues a message for this socket without allowing a slow client to
// block the caller. A full queue is treated as an unhealthy connection and is
// closed so the read loop can unwind and unregister it.
func (c *Client) Send(msg []byte) error {
	select {
	case <-c.done:
		return ErrClientClosed
	default:
	}

	select {
	case <-c.done:
		return ErrClientClosed
	case c.send <- msg:
		return nil
	default:
		c.abort()
		return ErrSendQueueFull
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case msg := <-c.send:
			if err := c.Conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				c.abort()
				return
			}
			if err := c.Conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				c.abort()
				return
			}
		case <-ticker.C:
			if err := c.Conn.WriteControl(
				websocket.PingMessage,
				nil,
				time.Now().Add(writeWait),
			); err != nil {
				c.abort()
				return
			}
		}
	}
}

// Close sends a WebSocket close control frame and closes the underlying
// connection. It is safe to call multiple times and concurrently with writes.
func (c *Client) Close(code int, reason string) error {
	var writeErr error
	c.closeOnce.Do(func() {
		writeErr = c.Conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(code, reason),
			time.Now().Add(writeWait),
		)
		close(c.done)
		_ = c.Conn.Close()
	})
	return writeErr
}

func (c *Client) abort() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.Conn.Close()
	})
}

type Hub struct {
	mu          sync.RWMutex
	connections map[string]map[*Client]struct{} // userID → active devices/tabs
	draining    bool
	active      sync.WaitGroup
}

func New() *Hub {
	return &Hub{
		connections: make(map[string]map[*Client]struct{}),
	}
}

// Register adds a socket unless shutdown has begun. Once the hub is draining,
// no new WebSocket is allowed to enter the active set.
func (h *Hub) Register(client *Client) bool {
	h.mu.Lock()
	if h.draining {
		h.mu.Unlock()
		_ = client.Close(websocket.CloseGoingAway, "server_shutdown")
		return false
	}
	if h.connections[client.UserID] == nil {
		h.connections[client.UserID] = make(map[*Client]struct{})
	}
	h.connections[client.UserID][client] = struct{}{}
	h.active.Add(1)
	h.mu.Unlock()

	client.start()
	return true
}

func (h *Hub) Unregister(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients := h.connections[client.UserID]
	if clients == nil {
		return
	}
	if _, ok := clients[client]; !ok {
		return
	}

	delete(clients, client)
	if len(clients) == 0 {
		delete(h.connections, client.UserID)
	}
	h.active.Done()
}

// Send writes to every active connection for the user and reports whether at
// least one delivery was queued successfully.
func (h *Hub) Send(userID string, msg []byte) bool {
	return h.SendChannel(userID, "", msg)
}

// SendChannel restricts delivery to a socket domain. An empty channel keeps
// the legacy behavior and delivers to every connection.
func (h *Hub) SendChannel(userID, channel string, msg []byte) bool {
	return h.SendRoute(userID, channel, "", msg)
}

// SendRoute additionally restricts delivery to sockets opened for a specific
// counterpart. This prevents messages from leaking into another open direct
// conversation while still allowing multiple tabs/devices for the same chat.
func (h *Hub) SendRoute(userID, channel, receiverID string, msg []byte) bool {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.connections[userID]))
	for client := range h.connections[userID] {
		if channel != "" && client.Channel != channel {
			continue
		}
		if receiverID != "" && client.ReceiverID != receiverID {
			continue
		}
		clients = append(clients, client)
	}
	h.mu.RUnlock()

	delivered := false
	for _, client := range clients {
		if err := client.Send(msg); err != nil {
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

// Shutdown prevents new registrations and closes every active WebSocket. The
// handlers themselves unregister in their deferred cleanup paths.
func (h *Hub) Shutdown(code int, reason string) {
	h.mu.Lock()
	h.draining = true
	clients := make([]*Client, 0)
	for _, userClients := range h.connections {
		for client := range userClients {
			clients = append(clients, client)
		}
	}
	h.mu.Unlock()

	for _, client := range clients {
		go func(client *Client) {
			_ = client.Close(code, reason)
		}(client)
	}
}

// Wait blocks until all registered WebSocket handlers have unregistered or
// the supplied context expires. Call Shutdown before Wait so no new clients
// can increment the active count.
func (h *Hub) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		h.active.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
