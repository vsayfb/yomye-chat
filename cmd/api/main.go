package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/vsayfb/gig-platform-chat-service/config"
	"github.com/vsayfb/gig-platform-chat-service/hub"
	"github.com/vsayfb/gig-platform-chat-service/internal/application"
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

func main() {
	ctx := context.Background()

	cfg, err := config.Load(ctx)

	if err != nil {
		log.Fatal(err)
	}

	logHandler := logger.Init(cfg.AppEnv)

	mongoClient, db, err := database.NewMongoDB(cfg.MongoURI, cfg.MongoDB)

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

	applicationSvc := application.NewApplicationService(applicationRepo)

	applicationHandler := application.NewApplicationHandler(applicationSvc)

	h := hub.New()

	jwtSvc := jwt.New(cfg.JWTSecret)

	awsClient, err := awsclient.New(ctx, cfg)

	if err != nil {
		slog.Error("failed to initialize aws client", "err", err)
		os.Exit(1)
	}

	slog.InfoContext(ctx, "aws client initialized")

	sqsClient := sqs.NewSQS(awsClient, cfg)

	sqsPublisher := sqs.NewPublisher(sqsClient, cfg.AWS.SQSNotificationQueueURL)

	slog.InfoContext(ctx, "publisher initialized")

	wsHandler := ws.NewWSHandler(h, jwtSvc, applicationSvc, sqsPublisher, userClient, gigClient)

	shutdownTelemetry, err := telemetry.Init(ctx, cfg.ServiceName, cfg.OTelCollectorAddr)

	if err != nil {
		slog.Error("failed to initialize telemetry", "err", err)
		os.Exit(1)
	}

	if err := metrics.Register(); err != nil {
		slog.Error("failed to register metrics", "err", err)
		os.Exit(1)
	}

	slog.SetDefault(slog.New(tracing.NewOTelHandler(logHandler)))

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

	r.Route("/applications", func(r chi.Router) {
		// TODO: implement a separate auth middleware for websocket upgrade
		authMiddleware := middleware.Auth(jwtSvc)

		r.With(authMiddleware).Get("/me", applicationHandler.ListApplications)
		r.With(authMiddleware).Get("/{threadID}", applicationHandler.GetApplication)
		r.With(authMiddleware).Get("/{threadID}/messages", applicationHandler.ListMessages)
		r.With(authMiddleware).Post("/{threadID}/approve", applicationHandler.ApproveApplication)
		r.With(authMiddleware).Post("/{threadID}/reject", applicationHandler.RejectApplication)
	})

	slog.InfoContext(ctx, "all dependencies injected")

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

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)

	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("failed to shutdown HTTP server", "err", err)
	}

	// if err := conn.Close(); err != nil {
	// 	slog.Error("failed to close gRPC connection", "err", err)
	// }

	if err := mongoClient.Disconnect(shutdownCtx); err != nil {
		slog.Error("failed to disconnect MongoDB", "err", err)
	}

	if err := shutdownTelemetry(shutdownCtx); err != nil {
		slog.Error("failed to shutdown telemetry", "err", err)
	}

	slog.Info("shutdown complete")
}
