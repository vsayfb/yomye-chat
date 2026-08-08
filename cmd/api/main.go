package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/gorilla/websocket"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/vsayfb/gig-platform-chat-service/config"
	"github.com/vsayfb/gig-platform-chat-service/hub"
	"github.com/vsayfb/gig-platform-chat-service/internal/application"
	"github.com/vsayfb/gig-platform-chat-service/internal/attachment"
	"github.com/vsayfb/gig-platform-chat-service/internal/conversation"
	"github.com/vsayfb/gig-platform-chat-service/internal/ws"
	"github.com/vsayfb/gig-platform-chat-service/pkg/awsclient"
	"github.com/vsayfb/gig-platform-chat-service/pkg/database"
	"github.com/vsayfb/gig-platform-chat-service/pkg/grpcclient"
	"github.com/vsayfb/gig-platform-chat-service/pkg/jwt"
	"github.com/vsayfb/gig-platform-chat-service/pkg/logger"
	"github.com/vsayfb/gig-platform-chat-service/pkg/metrics"
	"github.com/vsayfb/gig-platform-chat-service/pkg/middleware"
	"github.com/vsayfb/gig-platform-chat-service/pkg/objectstorage/r2"
	"github.com/vsayfb/gig-platform-chat-service/pkg/sqs"
	"github.com/vsayfb/gig-platform-chat-service/pkg/telemetry"
	"github.com/vsayfb/gig-platform-chat-service/pkg/tracing"
)

type telemetryState struct {
	mu       sync.RWMutex
	shutdown func(context.Context) error
}

const (
	httpReadHeaderTimeout = 5 * time.Second
	httpIdleTimeout       = 60 * time.Second
	httpMaxHeaderBytes    = 64 << 10
)

func (t *telemetryState) set(shutdown func(context.Context) error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.shutdown = shutdown
}

func (t *telemetryState) shutdownTelemetry(ctx context.Context) error {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.shutdown(ctx)
}

func initTelemetryWithRetry(
	ctx context.Context,
	cfg *config.Config,
	logHandler slog.Handler,
	state *telemetryState,
) {
	const retryInterval = 30 * time.Second

	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()

	for {
		shutdownTelemetry, err := telemetry.Init(
			ctx,
			cfg.ServiceName,
			cfg.OTelCollectorAddr,
			cfg.OTelTraceSampleRatio,
		)

		if err == nil {
			select {
			case <-ctx.Done():
				shutdownTelemetry(ctx)
				return
			default:
			}

			state.set(shutdownTelemetry)

			slog.SetDefault(
				slog.New(tracing.NewOTelHandler(logHandler)),
			)

			slog.Info(
				"telemetry initialized",
				"collector", cfg.OTelCollectorAddr,
				"trace_sample_ratio", cfg.OTelTraceSampleRatio,
			)
			return
		}

		slog.Warn(
			"telemetry unavailable, retrying",
			"retry_in", retryInterval,
			"error", err,
		)

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func runAttachmentCleanup(ctx context.Context, service *attachment.Service) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for {
		for {
			processed, err := service.CleanupOne(ctx)
			if err != nil {
				slog.ErrorContext(ctx, "attachment cleanup failed", "error", err)
				break
			}
			if !processed {
				break
			}
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func main() {
	ctx := context.Background()

	cfg, err := config.Load(ctx)
	if err != nil {
		log.Fatal(err)
	}

	logHandler := logger.Init(cfg.AppEnv)

	mongoClient, db, err := database.NewMongoDB(ctx, cfg.MongoURI, cfg.MongoDB)

	if err != nil {
		slog.Error("failed to connect to MongoDB", "err", err)
		os.Exit(1)
	}

	slog.InfoContext(ctx, "mongo connected")

	grpcClient, err := grpcclient.NewGRPCConnection(cfg.UserServiceGRPCAddr)
	if err != nil {
		slog.Error("failed to create grpc client", "err", err)
		os.Exit(1)
	}

	defer grpcClient.Close()

	gigClient := grpcclient.NewGigClient(grpcClient)
	userClient := grpcclient.NewUserClient(grpcClient)

	applicationRepo := application.NewApplicationRepository(db)

	if err := applicationRepo.EnsureIndexes(ctx); err != nil {
		slog.Error("failed to ensure application indexes", "err", err)
		os.Exit(1)
	}

	applicationSvc := application.NewApplicationService(applicationRepo)
	applicationHandler := application.NewApplicationHandler(applicationSvc)

	conversationRepo := conversation.NewRepository(db)

	if err := conversationRepo.EnsureIndexes(ctx); err != nil {
		slog.Error("failed to ensure conversation indexes", "err", err)
		os.Exit(1)
	}

	conversationSvc := conversation.NewService(conversationRepo, userClient)
	conversationHandler := conversation.NewHandler(conversationSvc)

	r2Client, err := r2.New(ctx, r2.Config{
		AccountID: cfg.R2.AccountID, AccessKeyID: cfg.R2.AccessKeyID,
		SecretAccessKey: cfg.R2.SecretAccessKey, Bucket: cfg.R2.Bucket,
		ObjectPrefix:      cfg.R2.ObjectPrefix,
		UploadURLExpiry:   cfg.R2.UploadURLExpiry,
		DownloadURLExpiry: cfg.R2.DownloadURLExpiry,
	})
	if err != nil {
		slog.Error("failed to initialize R2 client", "err", err)
		os.Exit(1)
	}

	attachmentRepo := attachment.NewRepository(db)
	if err := attachmentRepo.EnsureIndexes(ctx); err != nil {
		slog.Error("failed to ensure attachment indexes", "err", err)
		os.Exit(1)
	}
	authorizeAttachment := func(
		ctx context.Context,
		scope attachment.Scope,
		scopeID bson.ObjectID,
		userID string,
	) error {
		switch scope {
		case attachment.ScopeDirect:
			_, err := conversationSvc.Get(ctx, scopeID, userID)
			if errors.Is(err, conversation.ErrForbidden) {
				return attachment.ErrForbidden
			}
			if errors.Is(err, conversation.ErrNotFound) {
				return attachment.ErrNotFound
			}
			return err
		case attachment.ScopeApplication:
			_, err := applicationSvc.GetApplication(ctx, scopeID, userID)
			if errors.Is(err, application.ErrForbidden) {
				return attachment.ErrForbidden
			}
			if errors.Is(err, application.ErrApplicationNotFound) {
				return attachment.ErrNotFound
			}
			return err
		default:
			return attachment.ErrInvalidInput
		}
	}
	attachmentSvc := attachment.NewService(
		attachmentRepo, r2Client, mongoClient, authorizeAttachment,
	)
	attachmentHandler := attachment.NewHandler(attachmentSvc)

	h := hub.New()

	jwtSvc := jwt.New(cfg.JWTSecret)

	awsClient, err := awsclient.New(ctx, cfg)
	if err != nil {
		slog.Error("failed to initialize aws client", "err", err)
		os.Exit(1)
	}

	slog.InfoContext(ctx, "aws client initialized")

	sqsClient := sqs.NewSQS(awsClient, cfg)
	sqsPublisher := sqs.NewPublisher(
		sqsClient,
		cfg.AWS.SQSNotificationQueueURL,
	)

	wsHandler := ws.NewWSHandler(
		h,
		jwtSvc,
		applicationSvc,
		conversationSvc,
		attachmentSvc,
		sqsPublisher,
		userClient,
		gigClient,
	)

	telemetryCtx, telemetryCancel := context.WithCancel(ctx)

	telemetryState := &telemetryState{
		shutdown: func(context.Context) error {
			return nil
		},
	}

	go initTelemetryWithRetry(
		telemetryCtx,
		cfg,
		logHandler,
		telemetryState,
	)

	if err := metrics.Register(); err != nil {
		slog.Error("failed to register metrics", "err", err)
		os.Exit(1)
	}

	r := chi.NewRouter()

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{
			"http://localhost:3000",
		},
		AllowedMethods: []string{
			"GET",
			"POST",
			"PUT",
			"PATCH",
			"DELETE",
			"OPTIONS",
		},
		AllowedHeaders: []string{
			"Accept",
			"Authorization",
			"Content-Type",
			"X-CSRF-Token",
		},
		ExposedHeaders: []string{
			"Link",
		},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Use(chimiddleware.Heartbeat("/health"))
	r.Use(chimiddleware.RequestID)
	r.Use(middleware.Recovery)
	r.Use(middleware.Logger)
	r.Use(middleware.MetricsMiddleware)
	r.Use(middleware.TracingMiddleware)

	r.Get("/ws", wsHandler.ServeWS)
	r.Get("/ws/direct", wsHandler.ServeDirectWS)

	authMiddleware := middleware.Auth(jwtSvc)

	r.Route("/applications", func(r chi.Router) {
		r.With(authMiddleware).Get("/me", applicationHandler.ListApplications)
		r.With(authMiddleware).Get("/{threadID}", applicationHandler.GetApplication)
		r.With(authMiddleware).Get("/{threadID}/messages", applicationHandler.ListMessages)
		r.With(authMiddleware).Post("/{threadID}/approve", applicationHandler.ApproveApplication)
		r.With(authMiddleware).Post("/{threadID}/reject", applicationHandler.RejectApplication)
		r.With(authMiddleware).Post(
			"/{threadID}/attachments/uploads",
			attachmentHandler.AuthorizeApplicationUpload,
		)
	})

	r.Route("/conversations", func(r chi.Router) {
		r.Use(authMiddleware)
		r.Get("/me", conversationHandler.List)
		r.Get("/{conversationID}", conversationHandler.Get)
		r.Get("/{conversationID}/messages", conversationHandler.ListMessages)
		r.Post(
			"/{conversationID}/attachments/uploads",
			attachmentHandler.AuthorizeDirectUpload,
		)
	})

	r.Route("/attachments", func(r chi.Router) {
		r.Use(authMiddleware)
		r.Post("/{attachmentID}/confirm", attachmentHandler.Confirm)
		r.Get("/{attachmentID}/download", attachmentHandler.Download)
	})

	cleanupCtx, cleanupCancel := context.WithCancel(ctx)
	go runAttachmentCleanup(cleanupCtx, attachmentSvc)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
		MaxHeaderBytes:    httpMaxHeaderBytes,
	}

	serverErr := make(chan error, 1)

	go func() {
		slog.Info("Chat Service listening", "port", cfg.Port)
		serverErr <- srv.ListenAndServe()
	}()

	quit := make(chan os.Signal, 1)

	signal.Notify(
		quit,
		syscall.SIGINT,
		syscall.SIGTERM,
	)

	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server failed", "err", err)
		}

	case <-quit:
		slog.Info("shutting down chat service")
	}

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		15*time.Second,
	)
	defer cancel()

	// Stop background work before disconnecting any of its dependencies.
	cleanupCancel()
	telemetryCancel()

	// Shutdown closes the HTTP listeners first, preventing new upgrades. The
	// hub then enters drain mode, closes all hijacked WebSockets, and waits for
	// their handlers to leave before MongoDB is disconnected.
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("failed to shutdown HTTP server", "err", err)
	}

	h.Shutdown(websocket.CloseGoingAway, "server_shutdown")
	if err := h.Wait(shutdownCtx); err != nil {
		slog.Error("failed waiting for WebSocket handlers", "err", err)
	}

	if err := mongoClient.Disconnect(shutdownCtx); err != nil {
		slog.Error("failed to disconnect MongoDB", "err", err)
	}

	if err := telemetryState.shutdownTelemetry(shutdownCtx); err != nil {
		slog.Error("failed to shutdown telemetry", "err", err)
	}

	slog.Info("shutdown complete")
}
