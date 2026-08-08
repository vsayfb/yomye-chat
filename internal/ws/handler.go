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
	"github.com/vsayfb/gig-platform-chat-service/internal/attachment"
	"github.com/vsayfb/gig-platform-chat-service/internal/conversation"
	"github.com/vsayfb/gig-platform-chat-service/internal/messagecontent"
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

const (
	closeAccessTokenExpired = 4001
	closeReasonTokenExpired = "access_token_expired"
	channelApplication      = "application"
	channelDirect           = "direct"
	maxMessageLogicalLength = 4000
)

// incomingMessage is what the client sends over WebSocket.
type incomingMessage struct {
	ClientMessageID string   `json:"client_message_id,omitempty"`
	Content         string   `json:"content"`
	AttachmentIDs   []string `json:"attachment_ids,omitempty"`
}

// outgoingMessage is what we deliver to the recipient.
type outgoingMessage struct {
	Type        string           `json:"type,omitempty"`
	ThreadID    string           `json:"thread_id"`
	SenderID    string           `json:"sender_id"`
	Content     string           `json:"content"`
	Attachments []attachment.Ref `json:"attachments,omitempty"`
	SentAt      string           `json:"sent_at"`
}

type outgoingDirectMessage struct {
	Type           string           `json:"type"`
	ConversationID string           `json:"conversation_id"`
	SenderID       string           `json:"sender_id"`
	Content        string           `json:"content"`
	Attachments    []attachment.Ref `json:"attachments,omitempty"`
	SentAt         string           `json:"sent_at"`
}

type outgoingError struct {
	Type            string `json:"type"`
	ClientMessageID string `json:"client_message_id,omitempty"`
	Code            string `json:"code"`
	Message         string `json:"message"`
}

type outgoingAccepted struct {
	Type            string `json:"type"`
	ClientMessageID string `json:"client_message_id,omitempty"`
	MessageID       string `json:"message_id"`
	ThreadID        string `json:"thread_id,omitempty"`
	ConversationID  string `json:"conversation_id,omitempty"`
	SentAt          string `json:"sent_at"`
}

func validateMessageContent(content string) (code string, message string, ok bool) {
	if messagecontent.LogicalLength(content) > maxMessageLogicalLength {
		return "message_too_long", "Message content exceeds the 4000 character limit.", false
	}
	if err := messagecontent.Validate(content); err != nil {
		return "invalid_emoji", "Unsupported Yövmiye emoji.", false
	}
	return "", "", true
}

type WSHandler struct {
	hub             *hub.Hub
	jwtSvc          *jwt.Service
	applicationSvc  application.Service
	conversationSvc conversation.Service
	attachmentSvc   *attachment.Service
	userClient      *grpcclient.UserClient
	gigClient       *grpcclient.GigClient
	publisher       *mysqs.Publisher
}

func NewWSHandler(
	h *hub.Hub,
	jwtSvc *jwt.Service,
	applicationSvc application.Service,
	conversationSvc conversation.Service,
	attachmentSvc *attachment.Service,
	publisher *mysqs.Publisher,
	userClient *grpcclient.UserClient,
	gigClient *grpcclient.GigClient,
) *WSHandler {
	return &WSHandler{
		hub:             h,
		jwtSvc:          jwtSvc,
		applicationSvc:  applicationSvc,
		conversationSvc: conversationSvc,
		attachmentSvc:   attachmentSvc,
		publisher:       publisher,
		userClient:      userClient,
		gigClient:       gigClient,
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
		threadID bson.ObjectID
	)

	if threadIDParam := r.URL.Query().Get("threadID"); threadIDParam != "" {
		receiver, gig, err = h.resolveByThread(ctx, threadIDParam, sender.ID)
		if err == nil {
			threadID, _ = bson.ObjectIDFromHex(threadIDParam)
		}
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
	client := hub.NewClient(senderID, receiver.ID, channelApplication, conn)
	if !h.hub.Register(client) {
		return
	}

	slog.InfoContext(ctx, "ws: client connected", "userID", senderID, "ReceiverID", receiver.ID, "gigID", gig.ID)

	expiryTimer := time.AfterFunc(time.Until(claims.ExpiresAt.Time), func() {
		// Sending a close control frame gives clients a stable signal to refresh
		// and reconnect. Closing the connection then unblocks the read loop even
		// when the client is idle.
		_ = client.Close(closeAccessTokenExpired, closeReasonTokenExpired)
	})

	defer func() {
		expiryTimer.Stop()
		_ = client.Close(websocket.CloseNormalClosure, "")
		h.hub.Unregister(client)
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
			h.writeClientError(client, "", "invalid_message", "Invalid message format.")
			continue
		}

		attachmentIDs, parseErr := parseAttachmentIDs(incoming.AttachmentIDs)
		if parseErr != nil {
			h.writeClientError(
				client, incoming.ClientMessageID,
				"invalid_attachments", "Invalid attachment selection.",
			)
			continue
		}
		if incoming.Content == "" && len(attachmentIDs) == 0 {
			h.writeClientError(
				client,
				incoming.ClientMessageID,
				"empty_message",
				"Message content cannot be empty.",
			)
			continue
		}
		if code, message, ok := validateMessageContent(incoming.Content); !ok {
			h.writeClientError(
				client,
				incoming.ClientMessageID,
				code,
				message,
			)
			continue
		}

		h.handleMessage(
			client,
			*sender,
			*receiver,
			*gig,
			threadID,
			incoming.ClientMessageID,
			incoming.Content,
			attachmentIDs,
		)
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
	client *hub.Client,
	sender dto.Sender,
	receiver dto.Receiver,
	gig dto.Gig,
	threadID bson.ObjectID,
	clientMessageID string,
	content string,
	attachmentIDs []bson.ObjectID,
) {
	// Use a background context since the request context is cancelled when the
	// WebSocket disconnects, but persistence and notification should still complete.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if len(attachmentIDs) > 0 && threadID.IsZero() {
		h.writeClientError(
			client, clientMessageID, "attachments_require_thread",
			"Reconnect using threadID before sending attachments.",
		)
		return
	}

	var (
		msg         *application.Message
		isNewThread bool
	)
	err := h.attachmentSvc.CommitMessage(
		ctx, attachment.ScopeApplication, threadID, sender.ID, attachmentIDs,
		func(txCtx context.Context, refs []attachment.Ref) (bson.ObjectID, error) {
			var sendErr error
			msg, isNewThread, sendErr = h.applicationSvc.SendMessage(
				txCtx, sender.ID, receiver.ID, gig.ID, content, refs,
			)
			if sendErr != nil {
				return bson.NilObjectID, sendErr
			}
			return msg.ID, nil
		},
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

		if code, message, ok := attachmentMessageError(err); ok {
			h.writeClientError(client, clientMessageID, code, message)
		} else {
			h.sendMessageError(client, clientMessageID, err)
		}
		return
	}

	h.writeAccepted(client, outgoingAccepted{
		Type:            "message.accepted",
		ClientMessageID: clientMessageID,
		MessageID:       msg.ID.Hex(),
		ThreadID:        msg.ThreadID.Hex(),
		SentAt:          msg.SentAt.Format(time.RFC3339),
	})

	outgoing := outgoingMessage{
		Type:        "message",
		ThreadID:    msg.ThreadID.Hex(),
		SenderID:    msg.SenderID,
		Content:     msg.Content,
		Attachments: msg.Attachments,
		SentAt:      msg.SentAt.Format(time.RFC3339),
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
	if h.hub.SendChannel(receiver.ID, channelApplication, payload) {
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
			MessagePreview: notificationPreview(content, 30),
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

// ServeDirectWS handles casual, non-application direct conversations.
//
// First contact:
//
//	token=<jwt>, targetID=<uuid>
//
// Reconnect:
//
//	token=<jwt>, conversationID=<mongoObjectID>
func (h *WSHandler) ServeDirectWS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := r.URL.Query().Get("token")
	if token == "" {
		httputil.WriteError(w, http.StatusUnauthorized, "missing token")
		return
	}

	claims, err := h.jwtSvc.Verify(token)
	if err != nil {
		httputil.WriteError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	sender := dto.Sender{
		ID:        claims.Subject,
		Name:      claims.Name,
		AvatarURL: claims.AvatarURL,
	}

	targetID, err := h.resolveDirectTarget(ctx, r, sender.ID)
	if err != nil {
		h.writeDirectResolveError(w, ctx, err)
		return
	}
	var conversationID bson.ObjectID
	if value := r.URL.Query().Get("conversationID"); value != "" {
		conversationID, _ = bson.ObjectIDFromHex(value)
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.ErrorContext(ctx, "ws: direct upgrade failed", "error", err)
		return
	}

	client := hub.NewClient(sender.ID, targetID, channelDirect, conn)
	if !h.hub.Register(client) {
		return
	}

	expiryTimer := time.AfterFunc(time.Until(claims.ExpiresAt.Time), func() {
		_ = client.Close(closeAccessTokenExpired, closeReasonTokenExpired)
	})

	defer func() {
		expiryTimer.Stop()
		_ = client.Close(websocket.CloseNormalClosure, "")
		h.hub.Unregister(client)
		slog.InfoContext(ctx, "ws: direct client disconnected", "user_id", sender.ID)
	}()

	slog.InfoContext(ctx, "ws: direct client connected",
		"user_id", sender.ID, "target_id", targetID)

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(
				err,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure,
				closeAccessTokenExpired,
			) {
				slog.ErrorContext(ctx, "ws: direct read error", "user_id", sender.ID, "error", err)
			}
			break
		}

		var incoming incomingMessage
		if err := json.Unmarshal(raw, &incoming); err != nil {
			h.writeClientError(client, "", "invalid_message", "Invalid message format.")
			continue
		}
		attachmentIDs, parseErr := parseAttachmentIDs(incoming.AttachmentIDs)
		if parseErr != nil {
			h.writeClientError(
				client, incoming.ClientMessageID,
				"invalid_attachments", "Invalid attachment selection.",
			)
			continue
		}
		if incoming.Content == "" && len(attachmentIDs) == 0 {
			h.writeClientError(
				client,
				incoming.ClientMessageID,
				"empty_message",
				"Message content cannot be empty.",
			)
			continue
		}
		if code, message, ok := validateMessageContent(incoming.Content); !ok {
			h.writeClientError(
				client,
				incoming.ClientMessageID,
				code,
				message,
			)
			continue
		}
		h.handleDirectMessage(
			client,
			sender,
			targetID,
			conversationID,
			incoming.ClientMessageID,
			incoming.Content,
			attachmentIDs,
		)
	}
}

func (h *WSHandler) resolveDirectTarget(
	ctx context.Context,
	r *http.Request,
	senderID string,
) (string, error) {
	if idParam := r.URL.Query().Get("conversationID"); idParam != "" {
		id, err := bson.ObjectIDFromHex(idParam)
		if err != nil {
			return "", badRequest("invalid conversationID", err)
		}
		item, err := h.conversationSvc.Get(ctx, id, senderID)
		if err != nil {
			return "", err
		}
		return conversation.OtherDirectMember(item, senderID)
	}

	targetID := r.URL.Query().Get("targetID")
	if targetID == "" {
		return "", badRequest("missing targetID", nil)
	}
	if targetID == senderID {
		return "", &resolveError{status: http.StatusConflict, msg: "cannot connect to yourself"}
	}

	exists, err := h.conversationSvc.DirectConversationExists(ctx, senderID, targetID)
	if err != nil {
		return "", err
	}
	if exists {
		return targetID, nil
	}

	allowed, err := h.userClient.AllowsSocialDirectMessages(ctx, targetID)
	if err != nil {
		return "", internalError("internal server error", err)
	}
	if !allowed {
		return "", &resolveError{
			status: http.StatusForbidden,
			msg:    "user does not allow social direct messages",
		}
	}
	return targetID, nil
}

func (h *WSHandler) writeDirectResolveError(
	w http.ResponseWriter,
	ctx context.Context,
	err error,
) {
	var rerr *resolveError
	switch {
	case errors.As(err, &rerr):
		if rerr.cause != nil {
			slog.WarnContext(ctx, "ws: direct resolve failed", "error", rerr.cause)
		}
		httputil.WriteError(w, rerr.status, rerr.msg)
	case errors.Is(err, conversation.ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, "conversation not found")
	case errors.Is(err, conversation.ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, conversation.ErrDirectMessagesDisabled):
		httputil.WriteError(w, http.StatusForbidden, "user does not allow social direct messages")
	case errors.Is(err, conversation.ErrInvalidDirectConversation):
		httputil.WriteError(w, http.StatusConflict, "not a direct conversation")
	default:
		slog.ErrorContext(ctx, "ws: direct resolve failed", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal server error")
	}
}

func (h *WSHandler) handleDirectMessage(
	client *hub.Client,
	sender dto.Sender,
	targetID string,
	conversationID bson.ObjectID,
	clientMessageID string,
	content string,
	attachmentIDs []bson.ObjectID,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if len(attachmentIDs) > 0 && conversationID.IsZero() {
		h.writeClientError(
			client, clientMessageID, "attachments_require_conversation",
			"Reconnect using conversationID before sending attachments.",
		)
		return
	}

	var msg *conversation.Message
	err := h.attachmentSvc.CommitMessage(
		ctx, attachment.ScopeDirect, conversationID, sender.ID, attachmentIDs,
		func(txCtx context.Context, refs []attachment.Ref) (bson.ObjectID, error) {
			var sendErr error
			msg, _, sendErr = h.conversationSvc.SendDirectMessage(
				txCtx, sender.ID, targetID, content, refs,
			)
			if sendErr != nil {
				return bson.NilObjectID, sendErr
			}
			return msg.ID, nil
		},
	)
	if err != nil {
		slog.ErrorContext(ctx, "ws: send direct message failed",
			"sender_id", sender.ID, "target_id", targetID, "error", err)
		if code, message, ok := attachmentMessageError(err); ok {
			h.writeClientError(client, clientMessageID, code, message)
			return
		}
		code, message := directMessageError(err)
		h.writeClientError(client, clientMessageID, code, message)
		return
	}

	h.writeAccepted(client, outgoingAccepted{
		Type:            "message.accepted",
		ClientMessageID: clientMessageID,
		MessageID:       msg.ID.Hex(),
		ConversationID:  msg.ConversationID.Hex(),
		SentAt:          msg.SentAt.Format(time.RFC3339),
	})

	outgoing := outgoingDirectMessage{
		Type:           "message",
		ConversationID: msg.ConversationID.Hex(),
		SenderID:       msg.SenderID,
		Content:        msg.Content,
		Attachments:    msg.Attachments,
		SentAt:         msg.SentAt.Format(time.RFC3339),
	}
	payload, err := json.Marshal(outgoing)
	if err != nil {
		slog.ErrorContext(ctx, "ws: marshal direct message failed", "error", err)
		return
	}

	if h.hub.SendRoute(targetID, channelDirect, sender.ID, payload) {
		return
	}

	event := sqs.Envelope{
		Type: mysqs.EventNewMessage,
		Payload: mysqs.NewMessageNotificationEvent{
			RecipientID:    targetID,
			SenderID:       sender.ID,
			SenderName:     sender.Name,
			ThreadID:       outgoing.ConversationID,
			MessageID:      msg.ID.Hex(),
			MessagePreview: notificationPreview(content, 30),
			OccurredAt:     time.Now().UTC(),
		},
	}
	if err := h.publisher.PublishNewMessage(ctx, event); err != nil {
		slog.ErrorContext(ctx, "ws: publish direct notification failed",
			"conversation_id", outgoing.ConversationID, "error", err)
	}
}

func (h *WSHandler) writeClientError(
	client *hub.Client,
	clientMessageID string,
	code string,
	message string,
) {
	payload, err := json.Marshal(outgoingError{
		Type:            "error",
		ClientMessageID: clientMessageID,
		Code:            code,
		Message:         message,
	})
	if err != nil {
		return
	}
	_ = client.Send(payload)
}

func (h *WSHandler) writeAccepted(client *hub.Client, accepted outgoingAccepted) {
	payload, err := json.Marshal(accepted)
	if err != nil {
		return
	}
	_ = client.Send(payload)
}

func (h *WSHandler) sendMessageError(
	client *hub.Client,
	clientMessageID string,
	err error,
) {
	code, message := applicationMessageError(err)

	payload, marshalErr := json.Marshal(outgoingError{
		Type:            "error",
		ClientMessageID: clientMessageID,
		Code:            code,
		Message:         message,
	})
	if marshalErr != nil {
		return
	}
	_ = client.Send(payload)
}

func applicationMessageError(err error) (string, string) {
	switch {
	case errors.Is(err, application.ErrApplicationPending):
		return "application_pending", "Waiting for the recipient to approve your application."
	case errors.Is(err, application.ErrApplicationMustBeApproved):
		return "application_must_be_approved", "Approve the application before sending messages."
	case errors.Is(err, application.ErrApplicationRejected):
		return "application_rejected", "This application was rejected."
	case errors.Is(err, application.ErrApplicationArchived):
		return "application_archived", "This application was archived."
	case errors.Is(err, application.ErrForbidden):
		return "forbidden", "You cannot send a message in this thread."
	case errors.Is(err, application.ErrInvalidApplicationStatus):
		return "invalid_application_status", "This application cannot accept messages."
	case errors.Is(err, application.ErrApplicationNotFound):
		return "application_not_found", "Application conversation not found."
	default:
		return "send_failed", "Message could not be sent. Please try again."
	}
}

func directMessageError(err error) (string, string) {
	switch {
	case errors.Is(err, conversation.ErrDirectMessagesDisabled):
		return "direct_messages_disabled", "This user does not allow social direct messages."
	case errors.Is(err, conversation.ErrForbidden):
		return "forbidden", "You cannot send a message in this conversation."
	case errors.Is(err, conversation.ErrNotFound):
		return "conversation_not_found", "Conversation not found."
	case errors.Is(err, conversation.ErrInvalidDirectConversation):
		return "invalid_conversation", "This is not a valid direct conversation."
	default:
		return "send_failed", "Message could not be sent. Please try again."
	}
}

func attachmentMessageError(err error) (string, string, bool) {
	switch {
	case errors.Is(err, attachment.ErrTooMany):
		return "too_many_attachments", "A message can include at most two attachments.", true
	case errors.Is(err, attachment.ErrDuplicate):
		return "duplicate_attachment", "The same attachment cannot be included twice.", true
	case errors.Is(err, attachment.ErrInvalidState),
		errors.Is(err, attachment.ErrExpired):
		return "attachment_unavailable", "One or more attachments are no longer available.", true
	case errors.Is(err, attachment.ErrForbidden):
		return "attachment_forbidden", "You cannot use one or more attachments.", true
	default:
		return "", "", false
	}
}

func parseAttachmentIDs(values []string) ([]bson.ObjectID, error) {
	if len(values) > attachment.MaxPerMessage {
		return nil, attachment.ErrTooMany
	}
	result := make([]bson.ObjectID, 0, len(values))
	seen := make(map[bson.ObjectID]struct{}, len(values))
	for _, value := range values {
		id, err := bson.ObjectIDFromHex(value)
		if err != nil || id.IsZero() {
			return nil, attachment.ErrInvalidInput
		}
		if _, ok := seen[id]; ok {
			return nil, attachment.ErrDuplicate
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
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

func notificationPreview(content string, maxLength int) string {
	if content == "" {
		return "[Ek]"
	}
	return truncateForNotification(messagecontent.NotificationText(content), maxLength)
}
