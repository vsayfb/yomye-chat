//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/go-chi/chi/v5"
	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	pb "github.com/vsayfb/gig-platform-protos/contracts"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/vsayfb/gig-platform-chat-service/config"
	"github.com/vsayfb/gig-platform-chat-service/hub"
	"github.com/vsayfb/gig-platform-chat-service/internal/application"
	"github.com/vsayfb/gig-platform-chat-service/internal/attachment"
	"github.com/vsayfb/gig-platform-chat-service/internal/conversation"
	"github.com/vsayfb/gig-platform-chat-service/internal/ratelimit"
	"github.com/vsayfb/gig-platform-chat-service/internal/readiness"
	"github.com/vsayfb/gig-platform-chat-service/internal/ws"
	"github.com/vsayfb/gig-platform-chat-service/internal/wsticket"
	"github.com/vsayfb/gig-platform-chat-service/pkg/database"
	"github.com/vsayfb/gig-platform-chat-service/pkg/grpcclient"
	chatjwt "github.com/vsayfb/gig-platform-chat-service/pkg/jwt"
	"github.com/vsayfb/gig-platform-chat-service/pkg/middleware"
	chatsqs "github.com/vsayfb/gig-platform-chat-service/pkg/sqs"
)

const (
	integrationJWTSecret = "integration-jwt-secret"
	coreIssuer           = "yevmiye-core"
	accessTokenUse       = "access"
	allowedWebOrigin     = "https://chat.integration.example"
)

type testEnvOptions struct {
	allowAnyOrigin bool
	allowedOrigins []string
	ticketTTL      time.Duration
	core           *coreBehavior
	sqsStatus      int
	rateLimits     *config.RateLimitConfig
}

type testEnv struct {
	t *testing.T

	mongoClient *mongo.Client
	db          *mongo.Database

	appRepo    application.Repository
	appSvc     application.Service
	convRepo   conversation.Repository
	convSvc    conversation.Service
	attachRepo attachment.Repository
	attachSvc  *attachment.Service

	hub       *hub.Hub
	tickets   *wsticket.Store
	ready     *readiness.Handler
	server    *httptest.Server
	jwtSvc    *chatjwt.Service
	wsHandler *ws.WSHandler

	coreServer  *grpc.Server
	coreLis     net.Listener
	coreConn    *grpc.ClientConn
	sqsServer   *httptest.Server
	sqsRequests *atomic.Int64
}

func newTestEnv(t *testing.T, options testEnvOptions) *testEnv {
	t.Helper()

	mongoURI := os.Getenv("CHAT_INTEGRATION_MONGO_URI")
	if mongoURI == "" {
		t.Fatal("CHAT_INTEGRATION_MONGO_URI is required; run `make test-integration`")
	}

	if options.ticketTTL == 0 {
		options.ticketTTL = wsticket.DefaultTTL
	}
	if options.allowedOrigins == nil && !options.allowAnyOrigin {
		options.allowedOrigins = []string{allowedWebOrigin}
	}
	if options.sqsStatus == 0 {
		options.sqsStatus = http.StatusOK
	}
	if options.rateLimits == nil {
		options.rateLimits = &config.RateLimitConfig{
			HTTPReadPerMinute: 300, HTTPReadBurst: 60,
			HTTPWritePerMinute: 60, HTTPWriteBurst: 15,
			WSTicketPerMinute: 20, WSTicketBurst: 5,
			WSMessagePerSecond: 5, WSMessageBurst: 10,
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dbName := "chat_integration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	mongoClient, db, err := database.NewMongoDB(ctx, mongoURI, dbName)
	if err != nil {
		t.Fatalf("connect integration MongoDB: %v", err)
	}

	env := &testEnv{
		t:           t,
		mongoClient: mongoClient,
		db:          db,
		jwtSvc:      chatjwt.New(integrationJWTSecret),
		hub:         hub.New(),
		tickets:     wsticket.NewStore(options.ticketTTL),
	}

	env.appRepo = application.NewApplicationRepository(db)
	if err := env.appRepo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure application indexes: %v", err)
	}
	appSvc := application.NewApplicationService(env.appRepo)
	env.appSvc = appSvc

	env.convRepo = conversation.NewRepository(db)
	if err := env.convRepo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure conversation indexes: %v", err)
	}

	env.attachRepo = attachment.NewRepository(db)
	if err := env.attachRepo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure attachment indexes: %v", err)
	}

	var userClient *grpcclient.UserClient
	var gigClient *grpcclient.GigClient
	if options.core != nil {
		env.coreServer, env.coreLis = startCoreServer(t, *options.core)
		env.coreConn, err = grpcclient.NewGRPCConnection(env.coreLis.Addr().String())
		if err != nil {
			t.Fatalf("create Core gRPC client: %v", err)
		}
		userClient = grpcclient.NewUserClient(env.coreConn)
		gigClient = grpcclient.NewGigClient(env.coreConn)
	}

	env.convSvc = conversation.NewService(env.convRepo, userClient)
	env.attachSvc = attachment.NewService(env.attachRepo, nil, mongoClient, nil, nil)

	publisher, sqsServer, sqsRequests := newSQSPublisher(t, options.sqsStatus)
	env.sqsServer = sqsServer
	env.sqsRequests = sqsRequests

	httpReadLimiter := ratelimit.New(
		options.rateLimits.HTTPReadPerMinute, time.Minute, options.rateLimits.HTTPReadBurst,
	)
	httpWriteLimiter := ratelimit.New(
		options.rateLimits.HTTPWritePerMinute, time.Minute, options.rateLimits.HTTPWriteBurst,
	)
	wsTicketLimiter := ratelimit.New(
		options.rateLimits.WSTicketPerMinute, time.Minute, options.rateLimits.WSTicketBurst,
	)
	wsMessageLimiter := ratelimit.New(
		options.rateLimits.WSMessagePerSecond, time.Second, options.rateLimits.WSMessageBurst,
	)

	env.wsHandler = ws.NewWSHandler(
		env.hub,
		env.tickets,
		appSvc,
		env.convSvc,
		env.attachSvc,
		publisher,
		userClient,
		gigClient,
		options.allowAnyOrigin,
		options.allowedOrigins,
		wsMessageLimiter,
	)
	env.ready = readiness.NewHandler(mongoClient)

	appHandler := application.NewApplicationHandler(appSvc)
	convHandler := conversation.NewHandler(env.convSvc)

	router := chi.NewRouter()
	router.Use(middleware.Recovery)
	router.Get("/ready", env.ready.ServeHTTP)

	auth := middleware.Auth(env.jwtSvc)
	readRateLimit := middleware.RateLimitByUser(httpReadLimiter)
	writeRateLimit := middleware.RateLimitByUser(httpWriteLimiter)
	ticketRateLimit := middleware.RateLimitByUser(wsTicketLimiter)
	router.With(auth, ticketRateLimit).Post("/ws/ticket", env.wsHandler.IssueTicket)
	router.Get("/ws", env.wsHandler.ServeWS)
	router.Get("/ws/direct", env.wsHandler.ServeDirectWS)

	router.Route("/applications", func(r chi.Router) {
		r.Use(auth)
		r.With(readRateLimit).Get("/{threadID}", appHandler.GetApplication)
		r.With(readRateLimit).Get("/{threadID}/messages", appHandler.ListMessages)
		r.With(writeRateLimit).Post("/{threadID}/approve", appHandler.ApproveApplication)
		r.With(writeRateLimit).Post("/{threadID}/reject", appHandler.RejectApplication)
	})
	router.Route("/conversations", func(r chi.Router) {
		r.Use(auth)
		r.With(readRateLimit).Get("/{conversationID}", convHandler.Get)
		r.With(readRateLimit).Get("/{conversationID}/messages", convHandler.ListMessages)
	})

	env.server = httptest.NewServer(router)

	t.Cleanup(func() {
		if env.server != nil {
			env.server.Close()
		}
		env.hub.Shutdown(websocket.CloseGoingAway, "integration_test_shutdown")
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = env.hub.Wait(waitCtx)
		waitCancel()

		if env.coreConn != nil {
			_ = env.coreConn.Close()
		}
		if env.coreServer != nil {
			env.coreServer.Stop()
		}
		if env.coreLis != nil {
			_ = env.coreLis.Close()
		}
		if env.sqsServer != nil {
			env.sqsServer.Close()
		}

		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = env.db.Drop(cleanupCtx)
		_ = env.mongoClient.Disconnect(cleanupCtx)
	})

	return env
}

func newSQSPublisher(t *testing.T, responseStatus int) (*chatsqs.Publisher, *httptest.Server, *atomic.Int64) {
	t.Helper()

	requests := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		jsonProtocol := r.Header.Get("X-Amz-Target") != "" || strings.Contains(r.Header.Get("Content-Type"), "amz-json")
		if responseStatus >= 400 {
			if jsonProtocol {
				w.Header().Set("Content-Type", "application/x-amz-json-1.0")
			} else {
				w.Header().Set("Content-Type", "text/xml")
			}
			w.WriteHeader(responseStatus)
			if jsonProtocol {
				_, _ = w.Write([]byte(`{"__type":"InternalError","message":"integration failure"}`))
			} else {
				_, _ = w.Write([]byte(`<ErrorResponse><Error><Code>InternalError</Code><Message>integration failure</Message></Error></ErrorResponse>`))
			}
			return
		}
		if jsonProtocol {
			w.Header().Set("Content-Type", "application/x-amz-json-1.0")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"MessageId":"integration-message"}`))
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<SendMessageResponse><SendMessageResult><MessageId>integration-message</MessageId></SendMessageResult></SendMessageResponse>`))
	}))

	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("integration", "integration", ""),
	}
	client := awssqs.NewFromConfig(cfg, func(o *awssqs.Options) {
		o.BaseEndpoint = aws.String(server.URL)
		o.RetryMaxAttempts = 1
	})
	return chatsqs.NewPublisher(client, server.URL+"/queue/integration"), server, requests
}

type coreBehavior struct {
	Delay               time.Duration
	ErrorCode           codes.Code
	AllowDirectMessages bool
	MalformedUser       bool
	MismatchedUserID    string
	MissingUsers        bool
	MismatchedUsersID   string
	MissingGig          bool
}

func startCoreServer(t *testing.T, behavior coreBehavior) (*grpc.Server, net.Listener) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for Core gRPC test server: %v", err)
	}

	server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		if behavior.Delay > 0 {
			timer := time.NewTimer(behavior.Delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
		}
		if behavior.ErrorCode != codes.OK {
			return status.Error(behavior.ErrorCode, "integration Core failure")
		}

		method, ok := grpc.MethodFromServerStream(stream)
		if !ok {
			return status.Error(codes.Internal, "missing method")
		}

		switch {
		case strings.HasSuffix(method, "/GetUsers"):
			req := new(pb.GetUsersRequest)
			if err := stream.RecvMsg(req); err != nil {
				return err
			}
			if behavior.MissingUsers {
				return stream.SendMsg(&pb.GetUsersResponse{})
			}
			users := make([]*pb.User, 0, len(req.GetUserIds()))
			for _, id := range req.GetUserIds() {
				if behavior.MismatchedUsersID != "" {
					id = behavior.MismatchedUsersID
				}
				user := &pb.User{Id: id, Name: "integration-user"}
				data := protowire.AppendTag(nil, 4, protowire.VarintType)
				if behavior.AllowDirectMessages {
					data = protowire.AppendVarint(data, 1)
				} else {
					data = protowire.AppendVarint(data, 0)
				}
				user.ProtoReflect().SetUnknown(data)
				users = append(users, user)
			}
			return stream.SendMsg(&pb.GetUsersResponse{Users: users})

		case strings.HasSuffix(method, "/GetUser"):
			req := new(pb.GetUserRequest)
			if err := stream.RecvMsg(req); err != nil {
				return err
			}
			if behavior.MalformedUser {
				return stream.SendMsg(&pb.GetUserResponse{})
			}
			userID := req.GetUserId()
			if behavior.MismatchedUserID != "" {
				userID = behavior.MismatchedUserID
			}
			return stream.SendMsg(&pb.GetUserResponse{User: &pb.User{
				Id:        userID,
				Name:      "integration-user",
				AvatarUrl: "https://example.invalid/avatar.png",
			}})

		case strings.HasSuffix(method, "/GetGig"):
			req := new(pb.GetGigRequest)
			if err := stream.RecvMsg(req); err != nil {
				return err
			}
			if behavior.MissingGig {
				return stream.SendMsg(&pb.GetGigResponse{})
			}
			return stream.SendMsg(&pb.GetGigResponse{Gig: &pb.Gig{Title: "Integration Gig"}})

		default:
			return status.Errorf(codes.Unimplemented, "unhandled integration Core method %s", method)
		}
	}))

	go func() {
		_ = server.Serve(lis)
	}()

	return server, lis
}

type ticketEnvelope struct {
	Status int `json:"status"`
	Data   struct {
		Ticket    string `json:"ticket"`
		ExpiresAt string `json:"expires_at"`
	} `json:"data"`
}

type errorEnvelope struct {
	Status      int      `json:"status"`
	Data        struct{} `json:"data"`
	Error       string   `json:"error"`
	Description string   `json:"description"`
}

type acceptedMessage struct {
	Type            string `json:"type"`
	ClientMessageID string `json:"client_message_id"`
	MessageID       string `json:"message_id"`
	ThreadID        string `json:"thread_id"`
	ConversationID  string `json:"conversation_id"`
	SentAt          string `json:"sent_at"`
}

type wsError struct {
	Type            string `json:"type"`
	ClientMessageID string `json:"client_message_id"`
	Code            string `json:"code"`
	Message         string `json:"message"`
	RetryAfterMS    int64  `json:"retry_after_ms"`
}

type deliveredDirectMessage struct {
	Type           string           `json:"type"`
	ConversationID string           `json:"conversation_id"`
	SenderID       string           `json:"sender_id"`
	Content        string           `json:"content"`
	Attachments    []attachment.Ref `json:"attachments"`
}

type deliveredApplicationMessage struct {
	Type        string           `json:"type"`
	ThreadID    string           `json:"thread_id"`
	SenderID    string           `json:"sender_id"`
	Content     string           `json:"content"`
	Attachments []attachment.Ref `json:"attachments"`
}

func (e *testEnv) seedDirectConversation(t *testing.T, userA, userB string) *conversation.Conversation {
	t.Helper()
	participants := []string{userA, userB}
	sort.Strings(participants)
	now := time.Now().UTC()
	item := &conversation.Conversation{
		Type:      conversation.TypeDirect,
		DirectKey: strings.Join(participants, "\x00"),
		CreatedBy: userA,
		Members: []conversation.Member{
			{UserID: userA, Role: conversation.RoleMember, JoinedAt: now},
			{UserID: userB, Role: conversation.RoleMember, JoinedAt: now},
		},
		LastMessageAt: now,
	}
	if err := e.convRepo.Create(context.Background(), item); err != nil {
		t.Fatalf("seed direct conversation: %v", err)
	}
	return item
}

func (e *testEnv) seedApplication(t *testing.T, initiatorID, targetID, gigID string, status application.ApplicationStatus) *application.Application {
	t.Helper()
	now := time.Now().UTC()
	users := []string{initiatorID, targetID}
	sort.Strings(users)
	item := &application.Application{
		GigID:           gigID,
		ConversationKey: strings.Join([]string{gigID, users[0], users[1]}, "\x00"),
		InitiatorID:     initiatorID,
		TargetID:        targetID,
		Status:          status,
		LastMessageAt:   now,
	}
	if err := e.appRepo.CreateApplication(context.Background(), item); err != nil {
		t.Fatalf("seed application: %v", err)
	}
	return item
}

func (e *testEnv) issueTicket(t *testing.T, token, origin string) (string, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.server.URL+"/ws/ticket", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("issue WebSocket ticket: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return "", resp.StatusCode
	}
	var envelope ticketEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode ticket response: %v", err)
	}
	if envelope.Data.Ticket == "" {
		t.Fatal("ticket response was empty")
	}
	return envelope.Data.Ticket, resp.StatusCode
}

func (e *testEnv) mustIssueTicket(t *testing.T, token string) string {
	t.Helper()
	origin := ""
	if len(e.wsHandlerAllowedOrigins()) > 0 {
		origin = allowedWebOrigin
	}
	ticket, statusCode := e.issueTicket(t, token, origin)
	if statusCode != http.StatusCreated {
		t.Fatalf("ticket status = %d, want %d", statusCode, http.StatusCreated)
	}
	return ticket
}

func (e *testEnv) wsHandlerAllowedOrigins() []string {
	// The integration suite uses this only to choose the configured test origin.
	return []string{allowedWebOrigin}
}

func dialWebSocket(t *testing.T, baseURL, path string, query url.Values, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	endpoint := "ws" + strings.TrimPrefix(baseURL, "http") + path
	headers := http.Header{}
	if origin != "" {
		headers.Set("Origin", origin)
	}
	return websocket.DefaultDialer.Dial(endpoint+"?"+query.Encode(), headers)
}

func connectDirect(t *testing.T, e *testEnv, ticket, conversationID string) *websocket.Conn {
	t.Helper()
	conn, resp, err := dialWebSocket(t, e.server.URL, "/ws/direct", url.Values{
		"ticket":         []string{ticket},
		"conversationID": []string{conversationID},
	}, allowedWebOrigin)
	if err != nil {
		if resp != nil {
			t.Fatalf("connect direct WebSocket: %v (HTTP %d)", err, resp.StatusCode)
		}
		t.Fatalf("connect direct WebSocket: %v", err)
	}
	return conn
}

func connectApplicationThread(t *testing.T, e *testEnv, ticket, threadID string) *websocket.Conn {
	t.Helper()
	conn, resp, err := dialWebSocket(t, e.server.URL, "/ws", url.Values{
		"ticket":   []string{ticket},
		"threadID": []string{threadID},
	}, allowedWebOrigin)
	if err != nil {
		if resp != nil {
			t.Fatalf("connect application WebSocket: %v (HTTP %d)", err, resp.StatusCode)
		}
		t.Fatalf("connect application WebSocket: %v", err)
	}
	return conn
}

func connectApplicationParticipants(t *testing.T, e *testEnv, ticket, targetID, gigID string) *websocket.Conn {
	t.Helper()
	conn, resp, err := dialWebSocket(t, e.server.URL, "/ws", url.Values{
		"ticket":   []string{ticket},
		"targetID": []string{targetID},
		"gigID":    []string{gigID},
	}, allowedWebOrigin)
	if err != nil {
		if resp != nil {
			t.Fatalf("connect application first-contact WebSocket: %v (HTTP %d)", err, resp.StatusCode)
		}
		t.Fatalf("connect application first-contact WebSocket: %v", err)
	}
	return conn
}

func readAccepted(t *testing.T, conn *websocket.Conn) acceptedMessage {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var accepted acceptedMessage
	if err := conn.ReadJSON(&accepted); err != nil {
		t.Fatalf("read message.accepted: %v", err)
	}
	if accepted.Type != "message.accepted" || accepted.MessageID == "" {
		t.Fatalf("unexpected acknowledgement: %+v", accepted)
	}
	return accepted
}

func readDirectDelivery(t *testing.T, conn *websocket.Conn) deliveredDirectMessage {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var delivered deliveredDirectMessage
	if err := conn.ReadJSON(&delivered); err != nil {
		t.Fatalf("read direct recipient delivery: %v", err)
	}
	if delivered.Type != "message" {
		t.Fatalf("unexpected direct delivery: %+v", delivered)
	}
	return delivered
}

func readApplicationDelivery(t *testing.T, conn *websocket.Conn) deliveredApplicationMessage {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var delivered deliveredApplicationMessage
	if err := conn.ReadJSON(&delivered); err != nil {
		t.Fatalf("read application recipient delivery: %v", err)
	}
	if delivered.Type != "message" {
		t.Fatalf("unexpected application delivery: %+v", delivered)
	}
	return delivered
}

func readWSError(t *testing.T, conn *websocket.Conn) wsError {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var item wsError
	if err := conn.ReadJSON(&item); err != nil {
		t.Fatalf("read websocket error: %v", err)
	}
	if item.Type != "error" || item.Code == "" {
		t.Fatalf("unexpected websocket error: %+v", item)
	}
	return item
}

func waitConnected(t *testing.T, h *hub.Hub, userID string, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.IsConnected(userID) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("hub connected(%s) = %t, want %t", userID, h.IsConnected(userID), want)
}

func assertNoWebSocketMessage(t *testing.T, conn *websocket.Conn, wait time.Duration) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
		t.Fatal(err)
	}
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("unexpected websocket message")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("websocket became unusable while checking for duplicate/leaked delivery: %v", err)
	}
}

func assertCollectionCount(t *testing.T, collection *mongo.Collection, filter any, want int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := collection.CountDocuments(ctx, filter)
	if err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if got != want {
		t.Fatalf("document count = %d, want %d (filter=%v)", got, want, filter)
	}
}

func signedAccessToken(t *testing.T, subject, name string) string {
	t.Helper()
	return signedAccessTokenWith(t, subject, name, coreIssuer, accessTokenUse, time.Now().Add(10*time.Minute), gojwt.SigningMethodHS256, integrationJWTSecret)
}

func signedAccessTokenWith(
	t *testing.T,
	subject, name, issuer, tokenUse string,
	expiresAt time.Time,
	method gojwt.SigningMethod,
	secret string,
) string {
	t.Helper()
	claims := chatjwt.Claims{
		Name:     name,
		TokenUse: tokenUse,
		RegisteredClaims: gojwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   subject,
			ExpiresAt: gojwt.NewNumericDate(expiresAt),
		},
	}
	token, err := gojwt.NewWithClaims(method, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign access token: %v", err)
	}
	return token
}

func authenticatedRequest(t *testing.T, method, endpoint, token string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func doStatus(t *testing.T, req *http.Request) int {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func mustObjectID(t *testing.T, value string) bson.ObjectID {
	t.Helper()
	id, err := bson.ObjectIDFromHex(value)
	if err != nil {
		t.Fatalf("invalid ObjectID %q: %v", value, err)
	}
	return id
}

func describeDialError(err error, resp *http.Response) string {
	if resp != nil {
		return fmt.Sprintf("%v (HTTP %d)", err, resp.StatusCode)
	}
	return err.Error()
}
