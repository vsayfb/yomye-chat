package smoke

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestHTTPResponseDecoding(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-token" {
			t.Error("request omitted bearer authentication")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": http.StatusOK,
			"data": []map[string]any{{
				"id": "conversation-id", "type": "direct",
				"members": []map[string]string{{"user_id": "user-a"}, {"user_id": "user-b"}},
			}},
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	items, err := client.Conversations(context.Background(), "access-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "conversation-id" || len(items[0].Members) != 2 {
		t.Fatalf("Conversations() = %+v", items)
	}
}

func TestHTTPClientDoesNotForwardCredentialsThroughRedirects(t *testing.T) {
	t.Parallel()
	redirectReached := make(chan struct{}, 1)
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectReached <- struct{}{}
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer source.Close()

	client, err := NewClient(source.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Conversations(context.Background(), "access-token"); err == nil {
		t.Fatal("Conversations() followed a credential-bearing redirect")
	}
	select {
	case <-redirectReached:
		t.Fatal("redirect destination received the authenticated request")
	default:
	}
}

func TestWebSocketResponseDecoding(t *testing.T) {
	t.Parallel()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(Accepted{
			Type: "message.accepted", ClientMessageID: "client-id",
			MessageID: "message-id", ConversationID: "conversation-id", SentAt: time.Now().UTC(),
		})
		_ = conn.WriteJSON(Delivery{
			Type: "message", ConversationID: "conversation-id",
			SenderID: "user-a", Content: "Merhaba", SentAt: time.Now().UTC(),
		})
	}))
	defer server.Close()

	endpoint, _ := url.Parse(server.URL)
	endpoint.Scheme = "ws"
	conn, _, err := websocket.DefaultDialer.Dial(endpoint.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	accepted, err := ReadAccepted(conn, time.Second)
	if err != nil || accepted.MessageID != "message-id" {
		t.Fatalf("ReadAccepted() = %+v, %v", accepted, err)
	}
	delivery, err := ReadDelivery(conn, time.Second)
	if err != nil || delivery.Content != "Merhaba" {
		t.Fatalf("ReadDelivery() = %+v, %v", delivery, err)
	}
}

func TestReadAcceptedReturnsOnlySafeWebSocketErrorCode(t *testing.T) {
	t.Parallel()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(wsError{Type: "error", Code: "forbidden", Message: "private detail"})
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	endpoint.Scheme = "ws"
	conn, _, err := websocket.DefaultDialer.Dial(endpoint.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = ReadAccepted(conn, time.Second)
	if err == nil || err.Error() != "WebSocket rejected message: code=forbidden" {
		t.Fatalf("ReadAccepted() error = %v", err)
	}
}
