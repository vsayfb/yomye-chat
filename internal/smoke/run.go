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

func Run(ctx context.Context, chat *Client, core *CoreClient, manifest *Manifest) (*Summary, error) {
	if len(manifest.Users) < 3 {
		return nil, errors.New("scenario setup: at least three users are required")
	}
	if err := chat.Ready(ctx); err != nil {
		return nil, fmt.Errorf("readiness scenario: %w", err)
	}

	userA := manifest.Users[0]
	userB, listingID, err := selectListingOwner(ctx, core, manifest.Users[1:])
	if err != nil {
		return nil, err
	}
	userC := selectThirdUser(manifest.Users, userA.ID, userB.ID)
	if userC == nil {
		return nil, errors.New("scenario setup: no third user is available")
	}

	messageA := fmt.Sprintf("Smoke %s chat scenario 01: İlanınıza başvurmak istiyorum.", manifest.RunID)
	messageB := fmt.Sprintf("Smoke %s chat scenario 02: Başvurunuzu değerlendireceğim.", manifest.RunID)
	clientIDA := stableClientID(manifest.RunID, "application-a-to-b")
	clientIDB := stableClientID(manifest.RunID, "application-b-to-a")

	threadID, app, err := findApplication(ctx, chat, userA, userB, listingID)
	if err != nil {
		return nil, err
	}
	existingMessages := []ApplicationMessage(nil)
	if threadID != "" {
		existingMessages, err = chat.ApplicationMessages(
			ctx, userA.AccessToken, threadID, url.Values{"limit": []string{"100"}},
		)
		if err != nil {
			return nil, fmt.Errorf("existing application history scenario: %w", err)
		}
	}
	if countApplicationContent(existingMessages, messageA) > 1 ||
		countApplicationContent(existingMessages, messageB) > 1 {
		return nil, errors.New("idempotency scenario: duplicate smoke messages already exist")
	}

	connA, err := chat.OpenApplication(ctx, userA, userB.ID, listingID, threadID)
	if err != nil {
		return nil, fmt.Errorf("applicant connection scenario: %w", err)
	}
	defer closeSocket(connA)
	connB, err := chat.OpenApplication(ctx, userB, userA.ID, listingID, threadID)
	if err != nil {
		return nil, fmt.Errorf("listing-owner connection scenario: %w", err)
	}
	defer closeSocket(connB)

	realtime := 0
	ackA, deliveredA, err := sendApplicationAndVerify(
		connA, connB, clientIDA, messageA, userA.ID,
		countApplicationContent(existingMessages, messageA) == 1,
	)
	if err != nil {
		return nil, fmt.Errorf("applicant-to-owner realtime scenario: %w", err)
	}
	if deliveredA {
		realtime++
	}
	if threadID == "" {
		threadID = ackA.ThreadID
	}
	if ackA.ThreadID != threadID {
		return nil, errors.New("application identity scenario: acknowledgement used the wrong thread")
	}

	if app == nil || app.Status == "pending" {
		if err := chat.ApproveApplication(ctx, userB.AccessToken, threadID); err != nil {
			return nil, fmt.Errorf("application approval scenario: %w", err)
		}
	}

	ackB, deliveredB, err := sendApplicationAndVerify(
		connB, connA, clientIDB, messageB, userB.ID,
		countApplicationContent(existingMessages, messageB) == 1,
	)
	if err != nil {
		return nil, fmt.Errorf("owner-to-applicant realtime scenario: %w", err)
	}
	if deliveredB {
		realtime++
	}
	if ackB.ThreadID != threadID {
		return nil, errors.New("application reply identity scenario: acknowledgement used the wrong thread")
	}

	app, err = chat.Application(ctx, userB.AccessToken, threadID)
	if err != nil {
		return nil, fmt.Errorf("recipient application retrieval scenario: %w", err)
	}
	if app.GigID != listingID || app.InitiatorID != userA.ID ||
		app.TargetID != userB.ID || app.Status != "accepted" {
		return nil, errors.New("recipient application retrieval scenario: identity or status mismatch")
	}

	if err := retryApplication(connA, clientIDA, messageA, ackA); err != nil {
		return nil, fmt.Errorf("applicant message idempotency scenario: %w", err)
	}
	if err := retryApplication(connB, clientIDB, messageB, ackB); err != nil {
		return nil, fmt.Errorf("owner message idempotency scenario: %w", err)
	}

	history, err := chat.ApplicationMessages(
		ctx, userB.AccessToken, threadID, url.Values{"limit": []string{"100"}},
	)
	if err != nil {
		return nil, fmt.Errorf("persisted application history scenario: %w", err)
	}
	if err := verifyApplicationHistory(
		history, threadID, userA.ID, userB.ID, messageA, messageB, ackA, ackB,
	); err != nil {
		return nil, err
	}

	pageOne, err := chat.ApplicationMessages(
		ctx, userB.AccessToken, threadID, url.Values{"limit": []string{"1"}},
	)
	if err != nil || len(pageOne) != 1 {
		return nil, errors.New("pagination scenario: first page was not available")
	}
	pageTwo, err := chat.ApplicationMessages(ctx, userB.AccessToken, threadID, url.Values{
		"limit": []string{"1"}, "before": []string{pageOne[0].SentAt.Format(time.RFC3339Nano)},
		"before_id": []string{pageOne[0].ID},
	})
	if err != nil || len(pageTwo) != 1 || pageTwo[0].ID == pageOne[0].ID {
		return nil, errors.New("pagination scenario: composite cursor did not advance")
	}

	privatePath := "/applications/" + url.PathEscape(threadID)
	if err := chat.ExpectStatus(ctx, userC.AccessToken, privatePath, http.StatusForbidden); err != nil {
		return nil, fmt.Errorf("ownership read-isolation scenario: %w", err)
	}
	if conn, err := chat.OpenApplication(ctx, *userC, "", "", threadID); err == nil {
		closeSocket(conn)
		return nil, errors.New("ownership mutation-isolation scenario: third user opened the thread")
	}

	closeSocket(connA)
	closeSocket(connB)
	connA, connB = nil, nil
	reconnected, err := chat.OpenApplication(ctx, userA, "", "", threadID)
	if err != nil {
		return nil, fmt.Errorf("reconnection scenario: %w", err)
	}
	closeSocket(reconnected)
	if _, err := chat.ApplicationMessages(
		ctx, userA.AccessToken, threadID, url.Values{"limit": []string{"2"}},
	); err != nil {
		return nil, fmt.Errorf("post-reconnection history scenario: %w", err)
	}

	return &Summary{RunID: manifest.RunID, Users: 3, Conversations: 1, Messages: 2, Realtime: realtime}, nil
}

func selectListingOwner(ctx context.Context, core *CoreClient, users []User) (User, string, error) {
	for _, user := range users {
		listings, err := core.ListingsByAuthor(ctx, user.ID)
		if err != nil {
			return User{}, "", fmt.Errorf("Core listing discovery scenario: %w", err)
		}
		for _, listing := range listings {
			if listing.ID != "" && listing.Author.ID == user.ID {
				return user, listing.ID, nil
			}
		}
	}
	return User{}, "", errors.New("Core listing discovery scenario: cohort has no open listing")
}

func selectThirdUser(users []User, excluded ...string) *User {
	for i := range users {
		if users[i].ID != excluded[0] && users[i].ID != excluded[1] {
			return &users[i]
		}
	}
	return nil
}

func findApplication(
	ctx context.Context, chat *Client, applicant, owner User, listingID string,
) (string, *Application, error) {
	items, err := chat.Applications(ctx, applicant.AccessToken)
	if err != nil {
		return "", nil, fmt.Errorf("application discovery scenario: %w", err)
	}
	for _, item := range items {
		if item.GigID != listingID {
			continue
		}
		app, err := chat.Application(ctx, applicant.AccessToken, item.ThreadID)
		if err != nil {
			return "", nil, err
		}
		if app.InitiatorID == applicant.ID && app.TargetID == owner.ID {
			if app.Status != "pending" && app.Status != "accepted" {
				return "", nil, fmt.Errorf("application reuse scenario: status=%s", app.Status)
			}
			return item.ThreadID, app, nil
		}
	}
	return "", nil, nil
}

func sendApplicationAndVerify(
	sender, recipient *websocket.Conn, clientID, content, senderID string, replay bool,
) (*Accepted, bool, error) {
	if err := WriteMessage(sender, clientID, content); err != nil {
		return nil, false, err
	}
	ack, err := ReadApplicationAccepted(sender, 10*time.Second)
	if err != nil {
		return nil, false, err
	}
	if ack.ClientMessageID != clientID {
		return nil, false, errors.New("acknowledgement client_message_id mismatch")
	}
	if replay {
		return ack, false, nil
	}
	delivery, err := ReadApplicationDelivery(recipient, 10*time.Second)
	if err != nil {
		return nil, false, err
	}
	if delivery.SenderID != senderID || delivery.Content != content || delivery.ThreadID != ack.ThreadID {
		return nil, false, errors.New("real-time delivery identity or content mismatch")
	}
	return ack, true, nil
}

func retryApplication(conn *websocket.Conn, clientID, content string, original *Accepted) error {
	if err := WriteMessage(conn, clientID, content); err != nil {
		return err
	}
	retry, err := ReadApplicationAccepted(conn, 10*time.Second)
	if err != nil {
		return err
	}
	if retry.MessageID != original.MessageID || retry.ThreadID != original.ThreadID {
		return errors.New("retry changed durable message identity")
	}
	return nil
}

func verifyApplicationHistory(
	history []ApplicationMessage, threadID, userA, userB, messageA, messageB string,
	ackA, ackB *Accepted,
) error {
	if countApplicationContent(history, messageA) != 1 || countApplicationContent(history, messageB) != 1 {
		return errors.New("persistence/idempotency scenario: smoke messages are missing or duplicated")
	}
	positions := make(map[string]int, 2)
	for i, message := range history {
		if message.ThreadID != threadID {
			return errors.New("history identity scenario: wrong thread ID")
		}
		if message.ID == ackA.MessageID {
			if message.SenderID != userA || message.Content != messageA {
				return errors.New("history identity scenario: applicant message mismatch")
			}
			positions[message.ID] = i
		}
		if message.ID == ackB.MessageID {
			if message.SenderID != userB || message.Content != messageB {
				return errors.New("history identity scenario: owner reply mismatch")
			}
			positions[message.ID] = i
		}
	}
	if len(positions) != 2 || positions[ackB.MessageID] >= positions[ackA.MessageID] {
		return errors.New("message ordering scenario: reply is not newer than the first message")
	}
	return nil
}

func countApplicationContent(messages []ApplicationMessage, content string) int {
	count := 0
	for _, message := range messages {
		if message.Content == content {
			count++
		}
	}
	return count
}

func stableClientID(runID, scenario string) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + scenario))
	return fmt.Sprintf("smoke-%x", digest[:16])
}

func closeSocket(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	_ = conn.Close()
}
