package main

import (
	"context"
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

	"github.com/vsayfb/gig-platform-chat-service/config"
	"github.com/vsayfb/gig-platform-chat-service/hub"
	"github.com/vsayfb/gig-platform-chat-service/internal/application"
	"github.com/vsayfb/gig-platform-chat-service/internal/conversation"
	"github.com/vsayfb/gig-platform-chat-service/internal/ws"
	"github.com/vsayfb/gig-platform-chat-service/pkg/awsclient"
	"github.com/vsayfb/gig-platform-chat-service/pkg/database"
	"github.com/vsayfb/gig-platform-chat-service/pkg/grpcclient"
	"github.com/vsayfb/gig-platform-chat-service/pkg/jwt"
	"github.com/vsayfb/gig-platform-chat-service/pkg/logger"
	"github.com/vsayfb/gig-platform-chat-service/pkg/metrics"
	"github.com/vsayfb/gig-platform-chat-service/pkg/middleware"
	"github.com/vsayfb/gig-platform-chat-service/pkg/sqs"
	"github.com/vsayfb/gig-platform-chat-service/pkg/telemetry"
	"github.com/vsayfb/gig-platform-chat-service/pkg/tracing"
)

type telemetryState struct {
	mu       sync.RWMutex
	shutdown func(context.Context) error
}

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
	const (
		retryInterval = 30 * time.Second
		retryTimeout  = 5 * time.Minute
	)

	deadline := time.Now().Add(retryTimeout)

	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()

	for {
		shutdownTelemetry, err := telemetry.Init(
			ctx,
			cfg.ServiceName,
			cfg.OTelCollectorAddr,
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

			slog.Info("telemetry initialized")
			return
		}

		if time.Now().After(deadline) {
			slog.Error(
				"failed to initialize telemetry after retries",
				"error", err,
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

	conversationSvc := conversation.NewService(conversationRepo)
	conversationHandler := conversation.NewHandler(conversationSvc)

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
	})

	r.Route("/conversations", func(r chi.Router) {
		r.Use(authMiddleware)
		r.Get("/me", conversationHandler.List)
		r.Get("/{conversationID}", conversationHandler.Get)
		r.Get("/{conversationID}/messages", conversationHandler.ListMessages)
	})

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: r,
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

	telemetryCancel()

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("failed to shutdown HTTP server", "err", err)
	}

	if err := mongoClient.Disconnect(shutdownCtx); err != nil {
		slog.Error("failed to disconnect MongoDB", "err", err)
	}

	if err := telemetryState.shutdownTelemetry(shutdownCtx); err != nil {
		slog.Error("failed to shutdown telemetry", "err", err)
	}

	slog.Info("shutdown complete")
}
