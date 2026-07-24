package ws

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/vsayfb/gig-platform-chat-service/hub"
	"github.com/vsayfb/gig-platform-chat-service/internal/application"
	"github.com/vsayfb/gig-platform-chat-service/pkg/dto"
	"github.com/vsayfb/gig-platform-chat-service/pkg/grpcclient"
	"github.com/vsayfb/gig-platform-chat-service/pkg/httputil"
	"github.com/vsayfb/gig-platform-chat-service/pkg/jwt"
	"github.com/vsayfb/gig-platform-chat-service/pkg/sqs"

	mysqs "github.com/vsayfb/gig-platform-chat-service/pkg/sqs"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // tighten in production
	},
}

// incomingMessage is what the client sends over WebSocket.
type incomingMessage struct {
	Content string `json:"content"`
}

// outgoingMessage is what we deliver to the recipient.
type outgoingMessage struct {
	ThreadID string `json:"thread_id"`
	SenderID string `json:"sender_id"`
	Content  string `json:"content"`
	SentAt   string `json:"sent_at"`
}

type WSHandler struct {
	hub            *hub.Hub
	jwtSvc         *jwt.Service
	applicationSvc application.Service
	userClient     *grpcclient.UserClient
	gigClient      *grpcclient.GigClient
	publisher      *mysqs.Publisher
}

func NewWSHandler(
	h *hub.Hub,
	jwtSvc *jwt.Service,
	applicationSvc application.Service,
	publisher *mysqs.Publisher,
	userClient *grpcclient.UserClient,
	gigClient *grpcclient.GigClient,
) *WSHandler {
	return &WSHandler{
		hub:            h,
		jwtSvc:         jwtSvc,
		applicationSvc: applicationSvc,
		publisher:      publisher,
		userClient:     userClient,
		gigClient:      gigClient,
	}
}

// resolveError carries the HTTP status that should be written back to the
// client alongside the underlying cause, so callers of the resolve* helpers
// don't need to know about application-layer error semantics.
type resolveError struct {
	status int
	msg    string
	cause  error
}

func (e *resolveError) Error() string { return e.msg }
func (e *resolveError) Unwrap() error { return e.cause }

func badRequest(msg string, cause error) error {
	return &resolveError{status: http.StatusBadRequest, msg: msg, cause: cause}
}

func internalError(msg string, cause error) error {
	return &resolveError{status: http.StatusInternalServerError, msg: msg, cause: cause}
}

// ServeWS handles WebSocket upgrade and the read loop.
//
// Two ways to open a connection are supported:
//
//  1. First-contact mode — starts (or resumes, if one already exists) an
//     application between two participants for a gig:
//     token=<jwt>, targetID=<uuid>, gigID=<uuid>
//
//  2. Thread-resume mode — reconnects to an application that has already
//     been created, without needing to know the counterpart or gig again:
//     token=<jwt>, threadID=<mongoObjectID>
func (h *WSHandler) ServeWS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 1. Authenticate
	token := r.URL.Query().Get("token")

	if token == "" {
		slog.WarnContext(ctx, "invalid token", "err", token)

		httputil.WriteError(w, http.StatusUnauthorized, "missing token")
		return
	}

	claims, err := h.jwtSvc.Verify(token)

	if err != nil {
		slog.WarnContext(ctx, "invalid token", "err", err)

		httputil.WriteError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	senderID := claims.Subject

	sender := &dto.Sender{
		ID:        senderID,
		AvatarURL: claims.AvatarURL,
		Name:      claims.Name,
	}

	// 2. Resolve the receiver and gig, either from a threadID or from
	// targetID+gigID.
	var (
		receiver *dto.Receiver
		gig      *dto.Gig
	)

	if threadIDParam := r.URL.Query().Get("threadID"); threadIDParam != "" {
		receiver, gig, err = h.resolveByThread(ctx, threadIDParam, sender.ID)
	} else {
		receiver, gig, err = h.resolveByParticipants(ctx, r, sender.ID)
	}

	if err != nil {
		h.writeResolveError(w, ctx, err)
		return
	}

	// 3. Upgrade connection
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.ErrorContext(ctx, "ws: upgrade failed", "err", err)
		return
	}

	// 4. Register in Hub
	client := &hub.Client{
		UserID:     senderID,
		ReceiverID: receiver.ID,
		Conn:       conn,
	}

	h.hub.Register(client)

	slog.InfoContext(ctx, "ws: client connected", "userID", senderID, "ReceiverID", receiver.ID, "gigID", gig.ID)

	defer func() {
		h.hub.Unregister(senderID)
		conn.Close()
		slog.InfoContext(ctx, "ws: client disconnected", "userID", senderID)
	}()

	// 5. Read loop
	for {
		_, raw, err := conn.ReadMessage()

		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.ErrorContext(ctx, "ws: read error", "userID", senderID, "err", err)
			}
			break
		}

		var incoming incomingMessage

		if err := json.Unmarshal(raw, &incoming); err != nil {
			slog.WarnContext(ctx, "ws: invalid message format", "userID", senderID)
			continue
		}

		if incoming.Content == "" {
			continue
		}

		h.handleMessage(*sender, *receiver, *gig, incoming.Content)
	}
}

// resolveByParticipants handles first-contact mode: token=<jwt>,
// targetID=<uuid>, gigID=<uuid>. The application/thread may or may not
// exist yet — SendMessage creates it lazily on the first message.
func (h *WSHandler) resolveByParticipants(
	ctx context.Context,
	r *http.Request,
	senderID string,
) (*dto.Receiver, *dto.Gig, error) {

	receiverID := r.URL.Query().Get("targetID")

	if receiverID == "" {
		return nil, nil, badRequest("missing ReceiverID", nil)
	}

	if senderID == receiverID {
		return nil, nil, &resolveError{status: http.StatusConflict, msg: "cannot connect to yourself"}
	}

	resp, err := h.userClient.GetUser(ctx, receiverID)

	if err != nil {
		return nil, nil, internalError("internal server error", err)
	}

	receiver := &dto.Receiver{
		ID:        resp.User.GetId(),
		AvatarURL: resp.GetUser().AvatarUrl,
		Name:      resp.GetUser().Name,
	}

	gigID := r.URL.Query().Get("gigID")

	if gigID == "" {
		return nil, nil, badRequest("missing gigID", nil)
	}

	gigResp, err := h.gigClient.GetGig(ctx, gigID)

	if err != nil {
		return nil, nil, internalError("internal server error", err)
	}

	gig := &dto.Gig{
		ID:    gigID,
		Title: gigResp.Gig.GetTitle(),
	}

	return receiver, gig, nil
}

// resolveByThread handles thread-resume mode: token=<jwt>,
// threadID=<mongoObjectID>. The thread must already exist and the caller
// must be one of its two participants (enforced by GetApplication); the
// counterpart and gig IDs are derived from the stored application.
//
// Unlike resolveByParticipants, this does not call out to the user/gig
// gRPC services: the application document already vouches for both
// existing (they were validated the first time the thread was created),
// and nothing downstream of this call reads Receiver.Name, Receiver.AvatarURL,
// or Gig.Title — only the IDs are used. Skipping those two round-trips
// keeps reconnects cheap.
func (h *WSHandler) resolveByThread(
	ctx context.Context,
	threadIDParam string,
	senderID string,
) (*dto.Receiver, *dto.Gig, error) {

	threadID, err := bson.ObjectIDFromHex(threadIDParam)

	if err != nil {
		return nil, nil, badRequest("invalid threadID", err)
	}

	app, err := h.applicationSvc.GetApplication(ctx, threadID, senderID)

	if err != nil {
		// application.ErrApplicationNotFound / application.ErrForbidden are
		// mapped to their HTTP statuses in writeResolveError.
		return nil, nil, err
	}

	receiverID := app.InitiatorID

	if app.InitiatorID == senderID {
		receiverID = app.TargetID
	}

	receiver := &dto.Receiver{ID: receiverID}
	gig := &dto.Gig{ID: app.GigID}

	return receiver, gig, nil
}

// writeResolveError maps a resolve error to the appropriate HTTP response,
// logging at a severity matching the error's origin.
func (h *WSHandler) writeResolveError(w http.ResponseWriter, ctx context.Context, err error) {
	var rerr *resolveError

	if errors.As(err, &rerr) {
		if rerr.cause != nil {
			if rerr.status >= http.StatusInternalServerError {
				slog.ErrorContext(ctx, "ws: resolve failed", "err", rerr.cause)
			} else {
				slog.WarnContext(ctx, "ws: resolve failed", "err", rerr.cause)
			}
		} else {
			slog.WarnContext(ctx, "ws: resolve failed", "msg", rerr.msg)
		}

		httputil.WriteError(w, rerr.status, rerr.msg)
		return
	}

	switch {
	case errors.Is(err, application.ErrApplicationNotFound):
		slog.WarnContext(ctx, "ws: thread not found", "err", err)
		httputil.WriteError(w, http.StatusNotFound, "thread not found")

	case errors.Is(err, application.ErrForbidden):
		slog.WarnContext(ctx, "ws: forbidden", "err", err)
		httputil.WriteError(w, http.StatusForbidden, "forbidden")

	default:
		slog.ErrorContext(ctx, "ws: resolve failed", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error")
	}
}

func (h *WSHandler) handleMessage(
	sender dto.Sender,
	receiver dto.Receiver,
	gig dto.Gig,
	content string,
) {
	// Use a background context since the request context is cancelled when the
	// WebSocket disconnects, but persistence and notification should still complete.
	ctx := context.Background()

	msg, isNewThread, err := h.applicationSvc.SendMessage(
		ctx,
		sender.ID,
		receiver.ID,
		gig.ID,
		content,
	)

	if err != nil {
		slog.ErrorContext(
			ctx,
			"ws: send message failed",
			"sender_id", sender.ID,
			"receiver_id", receiver.ID,
			"gig_id", gig.ID,
			"error", err,
		)

		// TODO: Send structured error back to the sender.
		//
		// Example:
		// {
		//   "type": "error",
		//   "code": "application_pending",
		//   "message": "Waiting for the recipient to approve your application."
		// }

		return
	}

	outgoing := outgoingMessage{
		ThreadID: msg.ThreadID.Hex(),
		SenderID: msg.SenderID,
		Content:  msg.Content,
		SentAt:   msg.SentAt.Format(time.RFC3339),
	}

	payload, err := json.Marshal(outgoing)

	if err != nil {
		slog.ErrorContext(
			ctx,
			"ws: marshal outgoing message failed",
			"thread_id", outgoing.ThreadID,
			"error", err,
		)
		return
	}

	// Deliver immediately if the recipient is online.
	if h.hub.Send(receiver.ID, payload) {
		return
	}

	// Recipient is offline. Only publish a notification event when this
	// message just created the thread — i.e. a brand-new application the
	// target hasn't seen yet. Subsequent messages in an existing thread no
	// longer trigger an SQS publish.
	if !isNewThread {
		return
	}

	event := sqs.Envelope{
		Type: mysqs.EventNewMessage,
		Payload: mysqs.NewMessageNotificationEvent{
			RecipientID:    receiver.ID,
			SenderID:       sender.ID,
			SenderName:     sender.Name,
			ThreadID:       outgoing.ThreadID,
			GigID:          gig.ID,
			MessageID:      msg.ID.String(),
			MessagePreview: truncateForNotification(content, 30),
			OccurredAt:     time.Now(),
		}}

	err = h.publisher.PublishNewMessage(ctx, event)

	if err != nil {
		slog.ErrorContext(
			ctx,
			"ws: publish offline notification failed",
			"thread_id", outgoing.ThreadID,
			"receiver_id", receiver.ID,
			"error", err,
		)
	}
}

func truncateForNotification(content string, maxLength int) string {
	runes := []rune(content)

	if len(runes) <= maxLength {
		return content
	}

	if maxLength <= 3 {
		return string(runes[:maxLength])
	}

	return string(runes[:maxLength-3]) + "..."
}
