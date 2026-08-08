package hub

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSendRouteQueuesOnlyMatchingSockets(t *testing.T) {
	t.Parallel()

	h := New()
	matching := unitClient("user-a", "user-b", "direct")
	wrongReceiver := unitClient("user-a", "user-c", "direct")
	wrongChannel := unitClient("user-a", "user-b", "application")
	otherUser := unitClient("user-z", "user-b", "direct")
	h.connections["user-a"] = map[*Client]struct{}{
		matching:      {},
		wrongReceiver: {},
		wrongChannel:  {},
	}
	h.connections["user-z"] = map[*Client]struct{}{otherUser: {}}

	if !h.SendRoute("user-a", "direct", "user-b", []byte("hello")) {
		t.Fatal("SendRoute() reported no delivery")
	}

	assertQueued(t, matching, "hello")
	assertNotQueued(t, wrongReceiver)
	assertNotQueued(t, wrongChannel)
	assertNotQueued(t, otherUser)
}

func TestSendChannelWithEmptyChannelPreservesAllUserSockets(t *testing.T) {
	t.Parallel()

	h := New()
	first := unitClient("user-a", "user-b", "direct")
	second := unitClient("user-a", "user-c", "application")
	h.connections["user-a"] = map[*Client]struct{}{first: {}, second: {}}

	if !h.Send("user-a", []byte("broadcast")) {
		t.Fatal("Send() reported no delivery")
	}
	assertQueued(t, first, "broadcast")
	assertQueued(t, second, "broadcast")
}

func TestUnregisterIsIdempotentAndWaitCompletes(t *testing.T) {
	t.Parallel()

	h := New()
	client := unitClient("user-a", "user-b", "direct")
	h.connections["user-a"] = map[*Client]struct{}{client: {}}
	h.active.Add(1)

	h.Unregister(client)
	h.Unregister(client)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.Wait(ctx); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if h.IsConnected("user-a") {
		t.Fatal("user remained connected after unregister")
	}
}

func TestWaitHonorsContextDeadline(t *testing.T) {
	t.Parallel()

	h := New()
	h.active.Add(1)
	defer h.active.Done()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := h.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() error = %v, want context deadline exceeded", err)
	}
}

func unitClient(userID, receiverID, channel string) *Client {
	return &Client{
		UserID: userID, ReceiverID: receiverID, Channel: channel,
		send: make(chan []byte, 4), done: make(chan struct{}),
	}
}

func assertQueued(t *testing.T, client *Client, want string) {
	t.Helper()
	select {
	case got := <-client.send:
		if string(got) != want {
			t.Fatalf("queued message = %q, want %q", got, want)
		}
	default:
		t.Fatal("expected queued message")
	}
}

func assertNotQueued(t *testing.T, client *Client) {
	t.Helper()
	select {
	case got := <-client.send:
		t.Fatalf("unexpected queued message %q", got)
	default:
	}
}
