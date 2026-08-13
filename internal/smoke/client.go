package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

type Client struct {
	target *url.URL
	http   *http.Client
	dialer *websocket.Dialer
}

type successEnvelope[T any] struct {
	Status int `json:"status"`
	Data   T   `json:"data"`
}

type ticketData struct {
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Conversation struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Members []Member `json:"members"`
}

type Member struct {
	UserID string `json:"user_id"`
}

type Message struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	SenderID       string    `json:"sender_id"`
	Content        string    `json:"content"`
	SentAt         time.Time `json:"sent_at"`
}

type Accepted struct {
	Type            string    `json:"type"`
	ClientMessageID string    `json:"client_message_id"`
	MessageID       string    `json:"message_id"`
	ConversationID  string    `json:"conversation_id"`
	SentAt          time.Time `json:"sent_at"`
}

type Delivery struct {
	Type           string    `json:"type"`
	ConversationID string    `json:"conversation_id"`
	SenderID       string    `json:"sender_id"`
	Content        string    `json:"content"`
	SentAt         time.Time `json:"sent_at"`
}

type wsError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewClient(rawTarget string, timeout time.Duration) (*Client, error) {
	target, err := url.Parse(rawTarget)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return nil, errors.New("--target must be an absolute http or https origin")
	}
	if target.User != nil || target.RawQuery != "" || target.Fragment != "" ||
		(target.Path != "" && target.Path != "/") {
		return nil, errors.New("--target must contain only scheme, host, and optional port")
	}
	target.Path = ""
	return &Client{
		target: target,
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				// Never forward a Core access token to a redirected origin.
				return http.ErrUseLastResponse
			},
		},
		dialer: &websocket.Dialer{HandshakeTimeout: min(timeout, 15*time.Second)},
	}, nil
}

func (c *Client) Ready(ctx context.Context) error {
	return c.get(ctx, "/ready", "", nil)
}

func (c *Client) Conversations(ctx context.Context, token string) ([]Conversation, error) {
	var result []Conversation
	if err := c.get(ctx, "/conversations/me", token, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) Conversation(ctx context.Context, token, id string) (*Conversation, error) {
	var result Conversation
	if err := c.get(ctx, "/conversations/"+url.PathEscape(id), token, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) Messages(
	ctx context.Context,
	token string,
	conversationID string,
	query url.Values,
) ([]Message, error) {
	path := "/conversations/" + url.PathEscape(conversationID) + "/messages"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var result []Message
	if err := c.get(ctx, path, token, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) ExpectStatus(ctx context.Context, token, path string, expected int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.target.ResolveReference(&url.URL{Path: path}).String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != expected {
		return fmt.Errorf("HTTP status=%d want=%d path=%s", resp.StatusCode, expected, path)
	}
	return nil
}

func (c *Client) OpenDirect(
	ctx context.Context,
	user User,
	targetID string,
	conversationID string,
) (*websocket.Conn, error) {
	ticket, err := c.issueTicket(ctx, user.AccessToken)
	if err != nil {
		return nil, err
	}
	query := url.Values{"ticket": []string{ticket.Ticket}}
	if conversationID == "" {
		query.Set("targetID", targetID)
	} else {
		query.Set("conversationID", conversationID)
	}
	endpoint := *c.target
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	endpoint.Path = "/ws/direct"
	endpoint.RawQuery = query.Encode()

	conn, resp, err := c.dialer.DialContext(ctx, endpoint.String(), nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("WebSocket handshake failed: status=%d", status)
	}
	return conn, nil
}

func (c *Client) issueTicket(ctx context.Context, token string) (*ticketData, error) {
	endpoint := c.target.ResolveReference(&url.URL{Path: "/ws/ticket"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ticket request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("ticket request status=%d", resp.StatusCode)
	}
	var envelope successEnvelope[ticketData]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode ticket response: %w", err)
	}
	if envelope.Status != http.StatusCreated || envelope.Data.Ticket == "" ||
		!envelope.Data.ExpiresAt.After(time.Now()) {
		return nil, errors.New("ticket response is malformed or expired")
	}
	return &envelope.Data, nil
}

func (c *Client) get(ctx context.Context, path, token string, target any) error {
	endpoint, err := url.Parse(path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.target.ResolveReference(endpoint).String(), nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP status=%d path=%s", resp.StatusCode, endpoint.Path)
	}
	if target == nil {
		return nil
	}
	envelope := successEnvelope[json.RawMessage]{}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode HTTP response: %w", err)
	}
	if envelope.Status != http.StatusOK || len(envelope.Data) == 0 {
		return errors.New("HTTP response envelope is malformed")
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return fmt.Errorf("decode HTTP response data: %w", err)
	}
	return nil
}

func WriteMessage(conn *websocket.Conn, clientID, content string) error {
	return conn.WriteJSON(map[string]any{
		"client_message_id": clientID,
		"content":           content,
	})
}

func ReadAccepted(conn *websocket.Conn, timeout time.Duration) (*Accepted, error) {
	var raw json.RawMessage
	if err := readWS(conn, timeout, &raw); err != nil {
		return nil, err
	}
	var eventType struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &eventType); err != nil {
		return nil, fmt.Errorf("decode WebSocket event type: %w", err)
	}
	if eventType.Type == "error" {
		var event wsError
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, errors.New("decode WebSocket error")
		}
		return nil, fmt.Errorf("WebSocket rejected message: code=%s", event.Code)
	}
	var accepted Accepted
	if err := json.Unmarshal(raw, &accepted); err != nil {
		return nil, fmt.Errorf("decode accepted event: %w", err)
	}
	if accepted.Type != "message.accepted" || accepted.MessageID == "" ||
		accepted.ConversationID == "" || accepted.SentAt.IsZero() {
		return nil, errors.New("message.accepted event is malformed")
	}
	return &accepted, nil
}

func ReadDelivery(conn *websocket.Conn, timeout time.Duration) (*Delivery, error) {
	var delivery Delivery
	if err := readWS(conn, timeout, &delivery); err != nil {
		return nil, err
	}
	if delivery.Type != "message" || delivery.ConversationID == "" ||
		delivery.SenderID == "" || delivery.SentAt.IsZero() {
		return nil, errors.New("message delivery event is malformed")
	}
	return &delivery, nil
}

func readWS(conn *websocket.Conn, timeout time.Duration, target any) error {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	if err := conn.ReadJSON(target); err != nil {
		return fmt.Errorf("read WebSocket event: %w", err)
	}
	return nil
}

func FindDirect(conversations []Conversation, userA, userB string) *Conversation {
	for i := range conversations {
		if conversations[i].Type != "direct" || len(conversations[i].Members) != 2 {
			continue
		}
		seenA, seenB := false, false
		for _, member := range conversations[i].Members {
			seenA = seenA || member.UserID == userA
			seenB = seenB || member.UserID == userB
		}
		if seenA && seenB {
			return &conversations[i]
		}
	}
	return nil
}

func CountContent(messages []Message, content string) int {
	count := 0
	for _, message := range messages {
		if message.Content == content {
			count++
		}
	}
	return count
}
