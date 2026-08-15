package smoke

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

type ApplicationSummary struct {
	ThreadID string `json:"thread_id"`
	GigID    string `json:"gig_id"`
}

type Application struct {
	ID          string `json:"id"`
	GigID       string `json:"gig_id"`
	InitiatorID string `json:"initiator_id"`
	TargetID    string `json:"target_id"`
	Status      string `json:"status"`
}

type ApplicationMessage struct {
	ID       string    `json:"id"`
	ThreadID string    `json:"thread_id"`
	SenderID string    `json:"sender_id"`
	Content  string    `json:"content"`
	SentAt   time.Time `json:"sent_at"`
}

func (c *Client) Applications(ctx context.Context, token string) ([]ApplicationSummary, error) {
	var result []ApplicationSummary
	if err := c.get(ctx, "/applications/me", token, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) Application(ctx context.Context, token, threadID string) (*Application, error) {
	var result Application
	if err := c.get(ctx, "/applications/"+url.PathEscape(threadID), token, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) ApplicationMessages(
	ctx context.Context, token, threadID string, query url.Values,
) ([]ApplicationMessage, error) {
	path := "/applications/" + url.PathEscape(threadID) + "/messages"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var result []ApplicationMessage
	if err := c.get(ctx, path, token, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) ApproveApplication(ctx context.Context, token, threadID string) error {
	endpoint := c.target.ResolveReference(&url.URL{Path: "/applications/" + threadID + "/approve"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("approve application request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("approve application status=%d", resp.StatusCode)
	}
	return nil
}

func (c *Client) OpenApplication(
	ctx context.Context, user User, targetID, gigID, threadID string,
) (*websocket.Conn, error) {
	ticket, err := c.issueTicket(ctx, user.AccessToken)
	if err != nil {
		return nil, err
	}
	query := url.Values{"ticket": []string{ticket.Ticket}}
	if threadID == "" {
		query.Set("targetID", targetID)
		query.Set("gigID", gigID)
	} else {
		query.Set("threadID", threadID)
	}
	endpoint := *c.target
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	endpoint.Path = "/ws"
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

func ReadApplicationAccepted(conn *websocket.Conn, timeout time.Duration) (*Accepted, error) {
	accepted, err := ReadAccepted(conn, timeout)
	if err != nil {
		return nil, err
	}
	if accepted.ThreadID == "" {
		return nil, errors.New("message.accepted event has no thread_id")
	}
	return accepted, nil
}

func ReadApplicationDelivery(conn *websocket.Conn, timeout time.Duration) (*Delivery, error) {
	var delivery Delivery
	if err := readWS(conn, timeout, &delivery); err != nil {
		return nil, err
	}
	if delivery.Type != "message" || delivery.ThreadID == "" ||
		delivery.SenderID == "" || delivery.SentAt.IsZero() {
		return nil, errors.New("application message delivery event is malformed")
	}
	return &delivery, nil
}
