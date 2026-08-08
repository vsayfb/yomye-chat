package telemetry

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
)

func TestConnectCollectorWaitsUntilGRPCReady(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	server := grpc.NewServer()
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	conn, err := connectCollector(ctx, listener.Addr().String())
	if err != nil {
		t.Fatalf("connectCollector: %v", err)
	}
	defer conn.Close()
}

func TestInitRejectsInvalidTraceSampleRatioBeforeConnecting(t *testing.T) {
	tests := []float64{-0.01, 1.01}

	for _, ratio := range tests {
		t.Run("invalid_ratio", func(t *testing.T) {
			_, err := Init(
				context.Background(),
				"chat-service",
				"127.0.0.1:1",
				ratio,
			)
			if err == nil {
				t.Fatalf("Init(%v) unexpectedly succeeded", ratio)
			}
		})
	}
}
