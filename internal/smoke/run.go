package smoke

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

type Summary struct {
	RunID         string
	Users         int
	Conversations int
	Messages      int
	Realtime      int
}

func Run(ctx context.Context, client *Client, manifest *Manifest) (*Summary, error) {
	if len(manifest.Users) < 3 {
		return nil, errors.New("scenario setup: at least three users are required")
	}
	if err := client.Ready(ctx); err != nil {
		return nil, fmt.Errorf("readiness scenario: %w", err)
	}

	userA, userB, userC := manifest.Users[0], manifest.Users[1], manifest.Users[2]
	messageA := fmt.Sprintf("Smoke %s chat scenario 01: Merhaba, bugün nasılsın?", manifest.RunID)
	messageB := fmt.Sprintf("Smoke %s chat scenario 02: İyiyim, teşekkür ederim.", manifest.RunID)
	clientIDA := stableClientID(manifest.RunID, "direct-a-to-b")
	clientIDB := stableClientID(manifest.RunID, "direct-b-to-a")

	conversations, err := client.Conversations(ctx, userA.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("conversation discovery scenario: %w", err)
	}
	existing := FindDirect(conversations, userA.ID, userB.ID)
	conversationID := ""
	if existing != nil {
		conversationID = existing.ID
	}

	existingMessages := []Message(nil)
	if conversationID != "" {
		existingMessages, err = client.Messages(ctx, userA.AccessToken, conversationID, url.Values{"limit": []string{"100"}})
		if err != nil {
			return nil, fmt.Errorf("existing history scenario: %w", err)
		}
	}
	if CountContent(existingMessages, messageA) > 1 || CountContent(existingMessages, messageB) > 1 {
		return nil, errors.New("idempotency scenario: duplicate smoke messages already exist")
	}

	connA, err := client.OpenDirect(ctx, userA, userB.ID, conversationID)
	if err != nil {
		return nil, fmt.Errorf("user A direct connection scenario: %w", err)
	}
	defer closeSocket(connA)
	connB, err := client.OpenDirect(ctx, userB, userA.ID, conversationID)
	if err != nil {
		return nil, fmt.Errorf("user B direct connection scenario: %w", err)
	}
	defer closeSocket(connB)

	realtime := 0
	ackA, deliveredA, err := sendAndVerify(
		connA, connB, clientIDA, messageA, userA.ID,
		CountContent(existingMessages, messageA) == 1,
	)
	if err != nil {
		return nil, fmt.Errorf("A-to-B realtime scenario: %w", err)
	}
	if deliveredA {
		realtime++
	}
	if conversationID == "" {
		conversationID = ackA.ConversationID
	}
	if ackA.ConversationID != conversationID {
		return nil, errors.New("A-to-B identity scenario: acknowledgement used the wrong conversation")
	}

	ackB, deliveredB, err := sendAndVerify(
		connB, connA, clientIDB, messageB, userB.ID,
		CountContent(existingMessages, messageB) == 1,
	)
	if err != nil {
		return nil, fmt.Errorf("B-to-A realtime scenario: %w", err)
	}
	if deliveredB {
		realtime++
	}
	if ackB.ConversationID != conversationID {
		return nil, errors.New("B-to-A identity scenario: acknowledgement used the wrong conversation")
	}
	conversation, err := client.Conversation(ctx, userB.AccessToken, conversationID)
	if err != nil {
		return nil, fmt.Errorf("recipient conversation retrieval scenario: %w", err)
	}
	if FindDirect([]Conversation{*conversation}, userA.ID, userB.ID) == nil {
		return nil, errors.New("recipient conversation retrieval scenario: participant identities mismatch")
	}

	if err := retryAndVerify(connA, clientIDA, messageA, ackA); err != nil {
		return nil, fmt.Errorf("A-to-B idempotency scenario: %w", err)
	}
	if err := retryAndVerify(connB, clientIDB, messageB, ackB); err != nil {
		return nil, fmt.Errorf("B-to-A idempotency scenario: %w", err)
	}

	history, err := client.Messages(ctx, userB.AccessToken, conversationID, url.Values{"limit": []string{"100"}})
	if err != nil {
		return nil, fmt.Errorf("persisted history scenario: %w", err)
	}
	if err := verifyHistory(history, conversationID, userA.ID, userB.ID, messageA, messageB, ackA, ackB); err != nil {
		return nil, err
	}

	pageOne, err := client.Messages(ctx, userB.AccessToken, conversationID, url.Values{"limit": []string{"1"}})
	if err != nil || len(pageOne) != 1 {
		return nil, errors.New("pagination scenario: first page was not available")
	}
	pageTwo, err := client.Messages(ctx, userB.AccessToken, conversationID, url.Values{
		"limit":     []string{"1"},
		"before":    []string{pageOne[0].SentAt.Format(time.RFC3339Nano)},
		"before_id": []string{pageOne[0].ID},
	})
	if err != nil || len(pageTwo) != 1 || pageTwo[0].ID == pageOne[0].ID {
		return nil, errors.New("pagination scenario: composite cursor did not advance")
	}

	privatePath := "/conversations/" + url.PathEscape(conversationID)
	if err := client.ExpectStatus(ctx, userC.AccessToken, privatePath, http.StatusForbidden); err != nil {
		return nil, fmt.Errorf("ownership read-isolation scenario: %w", err)
	}
	if conn, err := client.OpenDirect(ctx, userC, "", conversationID); err == nil {
		closeSocket(conn)
		return nil, errors.New("ownership mutation-isolation scenario: third user opened the conversation")
	}

	closeSocket(connA)
	closeSocket(connB)
	connA = nil
	connB = nil
	reconnected, err := client.OpenDirect(ctx, userA, "", conversationID)
	if err != nil {
		return nil, fmt.Errorf("reconnection scenario: %w", err)
	}
	closeSocket(reconnected)
	if _, err := client.Messages(ctx, userA.AccessToken, conversationID, url.Values{"limit": []string{"2"}}); err != nil {
		return nil, fmt.Errorf("post-reconnection history scenario: %w", err)
	}

	return &Summary{
		RunID: manifest.RunID, Users: 3, Conversations: 1,
		Messages: 2, Realtime: realtime,
	}, nil
}

func sendAndVerify(
	sender *websocket.Conn,
	recipient *websocket.Conn,
	clientID string,
	content string,
	senderID string,
	replay bool,
) (*Accepted, bool, error) {
	if err := WriteMessage(sender, clientID, content); err != nil {
		return nil, false, err
	}
	ack, err := ReadAccepted(sender, 10*time.Second)
	if err != nil {
		return nil, false, err
	}
	if ack.ClientMessageID != clientID {
		return nil, false, errors.New("acknowledgement client_message_id mismatch")
	}
	if replay {
		return ack, false, nil
	}
	delivery, err := ReadDelivery(recipient, 10*time.Second)
	if err != nil {
		return nil, false, err
	}
	if delivery.SenderID != senderID || delivery.Content != content ||
		delivery.ConversationID != ack.ConversationID {
		return nil, false, errors.New("real-time delivery identity or content mismatch")
	}
	return ack, true, nil
}

func retryAndVerify(conn *websocket.Conn, clientID, content string, original *Accepted) error {
	if err := WriteMessage(conn, clientID, content); err != nil {
		return err
	}
	retry, err := ReadAccepted(conn, 10*time.Second)
	if err != nil {
		return err
	}
	if retry.MessageID != original.MessageID || retry.ConversationID != original.ConversationID {
		return errors.New("retry changed durable message identity")
	}
	return nil
}

func verifyHistory(
	history []Message,
	conversationID, userA, userB, messageA, messageB string,
	ackA, ackB *Accepted,
) error {
	if CountContent(history, messageA) != 1 || CountContent(history, messageB) != 1 {
		return errors.New("persistence/idempotency scenario: smoke messages are missing or duplicated")
	}
	positions := make(map[string]int, 2)
	for i, message := range history {
		if message.ConversationID != conversationID {
			return errors.New("history identity scenario: wrong conversation ID")
		}
		if message.ID == ackA.MessageID {
			if message.SenderID != userA || message.Content != messageA {
				return errors.New("history identity scenario: A-to-B message mismatch")
			}
			positions[message.ID] = i
		}
		if message.ID == ackB.MessageID {
			if message.SenderID != userB || message.Content != messageB {
				return errors.New("history identity scenario: B-to-A message mismatch")
			}
			positions[message.ID] = i
		}
	}
	if len(positions) != 2 || positions[ackB.MessageID] >= positions[ackA.MessageID] {
		return errors.New("message ordering scenario: reply is not newer than the first message")
	}
	return nil
}

func stableClientID(runID, scenario string) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + scenario))
	return fmt.Sprintf("smoke-%x", digest[:16])
}

func closeSocket(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second),
	)
	_ = conn.Close()
}
