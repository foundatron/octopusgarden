//go:build integration

package scenario

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func newIntegrationLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestIntegrationGRPCUnary(t *testing.T) {
	svc := getSharedService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	grpcExec := &GRPCExecutor{Target: svc.grpcTarget, Logger: newIntegrationLogger()}
	defer grpcExec.Close()

	step := Step{
		GRPC: &GRPCRequest{
			Service: "echo.EchoService",
			Method:  "Echo",
			Body:    `{"message": "hello"}`,
		},
	}
	out, err := grpcExec.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if out.CaptureSources[GRPCSourceStatus] != "OK" {
		t.Errorf("gRPC status = %q, want %q", out.CaptureSources[GRPCSourceStatus], "OK")
	}

	vars := make(map[string]string)
	if err := applyCaptures([]Capture{{Name: "msg", JSONPath: "$.message"}}, out, vars); err != nil {
		t.Fatalf("capture message: %v", err)
	}
	if !strings.Contains(vars["msg"], "hello") {
		t.Errorf("response message = %q, want it to contain %q", vars["msg"], "hello")
	}
}

func TestIntegrationGRPCClientStream(t *testing.T) {
	svc := getSharedService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	grpcExec := &GRPCExecutor{Target: svc.grpcTarget, Logger: newIntegrationLogger()}
	defer grpcExec.Close()

	step := Step{
		GRPC: &GRPCRequest{
			Service: "echo.EchoService",
			Method:  "CollectEcho",
			Stream: &GRPCStream{
				Messages: []string{
					`{"message": "alpha"}`,
					`{"message": "beta"}`,
					`{"message": "gamma"}`,
				},
			},
		},
	}
	out, err := grpcExec.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if out.CaptureSources[GRPCSourceStatus] != "OK" {
		t.Errorf("gRPC status = %q, want %q", out.CaptureSources[GRPCSourceStatus], "OK")
	}

	vars := make(map[string]string)
	if err := applyCaptures([]Capture{{Name: "count", JSONPath: "$.count"}}, out, vars); err != nil {
		t.Fatalf("capture count: %v", err)
	}
	if vars["count"] != "3" {
		t.Errorf("count = %q, want %q", vars["count"], "3")
	}
}

func TestIntegrationGRPCServerStream(t *testing.T) {
	svc := getSharedService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	grpcExec := &GRPCExecutor{Target: svc.grpcTarget, Logger: newIntegrationLogger()}
	defer grpcExec.Close()

	step := Step{
		GRPC: &GRPCRequest{
			Service: "echo.EchoService",
			Method:  "StreamEcho",
			Body:    `{"message": "ping", "count": 3}`,
			Stream: &GRPCStream{
				Receive: &GRPCReceive{
					Count:   3,
					Timeout: "10s",
				},
			},
		},
	}
	out, err := grpcExec.Execute(ctx, step, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if out.CaptureSources[GRPCSourceStatus] != "OK" {
		t.Errorf("gRPC status = %q, want %q", out.CaptureSources[GRPCSourceStatus], "OK")
	}

	// CaptureBody is a JSON array of response messages.
	if !strings.Contains(out.CaptureBody, "ping") {
		t.Errorf("CaptureBody should contain 'ping', got: %s", out.CaptureBody)
	}
	// Should have received 3 messages (3 occurrences of "ping").
	if count := strings.Count(out.CaptureBody, "ping"); count < 3 {
		t.Errorf("expected 3 'ping' occurrences in CaptureBody, got %d: %s", count, out.CaptureBody)
	}
}

func TestIntegrationGRPCReflection(t *testing.T) {
	svc := getSharedService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	grpcExec := &GRPCExecutor{Target: svc.grpcTarget, Logger: newIntegrationLogger()}
	defer grpcExec.Close()

	// Request a method on a non-existent service — reflection should return an error.
	step := Step{
		GRPC: &GRPCRequest{
			Service: "nonexistent.FakeService",
			Method:  "NoMethod",
			Body:    `{}`,
		},
	}
	_, err := grpcExec.Execute(ctx, step, nil)
	if err == nil {
		t.Error("expected an error for non-existent service, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent.FakeService") {
		t.Errorf("error should mention the service name, got: %v", err)
	}
}
