package scenario

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestGRPCStepType(t *testing.T) {
	step := Step{GRPC: &GRPCRequest{Service: "svc", Method: "m"}}
	if got := step.StepType(); got != "grpc" {
		t.Errorf("StepType() = %q, want %q", got, "grpc")
	}
}

func TestSubstituteGRPCRequest(t *testing.T) {
	req := GRPCRequest{
		Service: "{svc_name}",
		Method:  "{method_name}",
		Body:    `{"id": "{sensor_id}"}`,
		Headers: map[string]string{"x-token": "{token}"},
		Stream: &GRPCStream{
			Messages: []string{`{"val": "{v}"}`},
		},
	}
	vars := map[string]string{
		"svc_name":    "telemetry.Service",
		"method_name": "Register",
		"sensor_id":   "s1",
		"token":       "abc",
		"v":           "42",
	}

	out := substituteGRPCRequest(req, vars)

	if out.Service != "telemetry.Service" {
		t.Errorf("Service = %q, want %q", out.Service, "telemetry.Service")
	}
	if out.Method != "Register" {
		t.Errorf("Method = %q, want %q", out.Method, "Register")
	}
	if out.Body != `{"id": "s1"}` {
		t.Errorf("Body = %q, want %q", out.Body, `{"id": "s1"}`)
	}
	if out.Headers["x-token"] != "abc" {
		t.Errorf("Headers[x-token] = %q, want %q", out.Headers["x-token"], "abc")
	}
	if out.Stream == nil || len(out.Stream.Messages) != 1 || out.Stream.Messages[0] != `{"val": "42"}` {
		t.Errorf("Stream.Messages = %v, want [{\"val\": \"42\"}]", out.Stream)
	}
}

func TestParseGRPCTimeout(t *testing.T) {
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"", defaultGRPCTimeout, false},
		{"5s", 5 * time.Second, false},
		{"100ms", 100 * time.Millisecond, false},
		{"notaduration", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseGRPCTimeout(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got %v", tt.wantErr, err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateGRPCRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     GRPCRequest
		wantErr error
	}{
		{
			name:    "valid",
			req:     GRPCRequest{Service: "svc", Method: "m"},
			wantErr: nil,
		},
		{
			name:    "missing service",
			req:     GRPCRequest{Method: "m"},
			wantErr: errGRPCMissingService,
		},
		{
			name:    "missing method",
			req:     GRPCRequest{Service: "svc"},
			wantErr: errGRPCMissingMethod,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGRPCRequest(tt.req)
			if err != tt.wantErr {
				t.Errorf("got %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestGRPCValidCaptureSources(t *testing.T) {
	exec := &GRPCExecutor{}
	sources := exec.ValidCaptureSources()
	want := map[string]bool{"status": true, "headers": true}
	if len(sources) != len(want) {
		t.Fatalf("got %v, want %v", sources, want)
	}
	for _, s := range sources {
		if !want[s] {
			t.Errorf("unexpected source %q", s)
		}
	}
}

func TestGRPCCloseIdempotent(t *testing.T) {
	exec := &GRPCExecutor{}
	// Should not panic on a never-initialized executor.
	exec.Close()
	exec.Close()
}

// buildTestMethodDesc creates a minimal protoreflect.MethodDescriptor for testing.
// The method belongs to service "test.pkg.TestService" with method name methodName.
func buildTestMethodDesc(t *testing.T, methodName string) protoreflect.MethodDescriptor {
	t.Helper()

	fdProto := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("test.proto"),
		Package: proto.String("test.pkg"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Req")},
			{Name: proto.String("Resp")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("TestService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String(methodName),
						InputType:  proto.String(".test.pkg.Req"),
						OutputType: proto.String(".test.pkg.Resp"),
					},
				},
			},
		},
	}

	fd, err := protodesc.NewFile(fdProto, nil)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}

	svc := fd.Services().ByName("TestService")
	if svc == nil {
		t.Fatal("service TestService not found in descriptor")
	}
	md := svc.Methods().ByName(protoreflect.Name(methodName))
	if md == nil {
		t.Fatalf("method %s not found in service", methodName)
	}
	return md
}

func TestFullMethodPath(t *testing.T) {
	tests := []struct {
		name       string
		methodName string
		want       string
	}{
		{
			name:       "standard method",
			methodName: "DoThing",
			want:       "/test.pkg.TestService/DoThing",
		},
		{
			name:       "single char method",
			methodName: "X",
			want:       "/test.pkg.TestService/X",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md := buildTestMethodDesc(t, tt.methodName)
			got, err := fullMethodPath(md)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("fullMethodPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCollectBackground(t *testing.T) {
	tests := []struct {
		name        string
		streamID    string
		bgStreams   map[string]*backgroundStream
		wantErr     error
		wantMsgSub  string // substring expected in CaptureBody
		checkOutput func(t *testing.T, out StepOutput)
	}{
		{
			name:     "stream not found",
			streamID: "nonexistent",
			bgStreams: map[string]*backgroundStream{
				"other-stream": {done: make(chan struct{})},
			},
			wantErr: errGRPCStreamNotFound,
		},
		{
			name:      "stream not found with empty map",
			streamID:  "missing",
			bgStreams: map[string]*backgroundStream{},
			wantErr:   errGRPCStreamNotFound,
		},
		{
			name:     "collect buffered messages",
			streamID: "stream-1",
			bgStreams: func() map[string]*backgroundStream {
				bg := &backgroundStream{
					messages: []string{`{"temp":22}`, `{"temp":23}`},
					done:     make(chan struct{}),
				}
				return map[string]*backgroundStream{"stream-1": bg}
			}(),
			wantErr:    nil,
			wantMsgSub: `{"temp":22}`,
			checkOutput: func(t *testing.T, out StepOutput) {
				t.Helper()
				// CaptureSources must be populated.
				if out.CaptureSources == nil {
					t.Fatal("CaptureSources is nil")
				}
				if _, ok := out.CaptureSources[GRPCSourceStatus]; !ok {
					t.Error("CaptureSources missing status key")
				}
				if _, ok := out.CaptureSources[GRPCSourceHeaders]; !ok {
					t.Error("CaptureSources missing headers key")
				}
				// Status should be OK since no background error.
				if got := out.CaptureSources[GRPCSourceStatus]; got != "OK" {
					t.Errorf("status = %q, want %q", got, "OK")
				}
				// CaptureBody should be a JSON array.
				if !strings.HasPrefix(out.CaptureBody, "[") || !strings.HasSuffix(out.CaptureBody, "]") {
					t.Errorf("CaptureBody not a JSON array: %q", out.CaptureBody)
				}
				// Observed should mention the stream ID.
				if !strings.Contains(out.Observed, "stream-1") {
					t.Errorf("Observed missing stream ID: %q", out.Observed)
				}
			},
		},
		{
			name:     "collect with count limiting",
			streamID: "stream-2",
			bgStreams: func() map[string]*backgroundStream {
				bg := &backgroundStream{
					messages: []string{`{"a":1}`, `{"a":2}`, `{"a":3}`},
					done:     make(chan struct{}),
				}
				return map[string]*backgroundStream{"stream-2": bg}
			}(),
			wantErr: nil,
			checkOutput: func(t *testing.T, out StepOutput) {
				t.Helper()
				// With count=1 (default when Receive is nil), only 1 message should be taken.
				// The remaining 2 should stay in the buffer.
				if !strings.Contains(out.CaptureBody, `{"a":1}`) {
					t.Errorf("expected first message in CaptureBody: %q", out.CaptureBody)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &GRPCExecutor{
				bgStreams: tt.bgStreams,
			}

			req := GRPCRequest{
				Stream: &GRPCStream{
					ID: tt.streamID,
					Receive: &GRPCReceive{
						Count:   1,
						Timeout: "100ms",
					},
				},
			}

			out, err := exec.collectBackground(req)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantMsgSub != "" && !strings.Contains(out.CaptureBody, tt.wantMsgSub) {
				t.Errorf("CaptureBody %q missing %q", out.CaptureBody, tt.wantMsgSub)
			}
			if tt.checkOutput != nil {
				tt.checkOutput(t, out)
			}
		})
	}
}

func TestExecuteNoConnection(t *testing.T) {
	// Execute with a valid service+method but no connection should fail
	// during ensureConnection or resolveMethod — not panic.
	exec := &GRPCExecutor{
		Target: "dns:///localhost:0", // will fail to connect/resolve
	}
	defer exec.Close()

	step := Step{
		GRPC: &GRPCRequest{
			Service: "test.Service",
			Method:  "DoThing",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := exec.Execute(ctx, step, nil)
	if err == nil {
		t.Fatal("expected error when no server is running, got nil")
	}
	// The error should come from the resolve step (reflection call fails).
	if !strings.Contains(err.Error(), "grpc") {
		t.Errorf("expected grpc-related error, got: %v", err)
	}
}

func TestExecuteDispatchCollectBackground(t *testing.T) {
	// Execute with stream.ID set and no service should dispatch to collectBackground.
	// Pre-populate bgStreams so collectBackground can find the stream.
	bgDone := make(chan struct{})
	close(bgDone) // already finished — no goroutine backing this stream
	bg := &backgroundStream{
		messages: []string{`{"collected":true}`},
		done:     bgDone,
	}

	exec := &GRPCExecutor{
		bgStreams: map[string]*backgroundStream{
			"my-stream": bg,
		},
	}
	defer exec.Close()

	step := Step{
		GRPC: &GRPCRequest{
			Stream: &GRPCStream{
				ID: "my-stream",
				Receive: &GRPCReceive{
					Count:   1,
					Timeout: "100ms",
				},
			},
			// Service intentionally empty — triggers collectBackground path.
		},
	}

	out, err := exec.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify it went through collectBackground (output mentions the stream ID).
	if !strings.Contains(out.Observed, "my-stream") {
		t.Errorf("Observed %q does not mention stream ID", out.Observed)
	}
	if !strings.Contains(out.CaptureBody, `{"collected":true}`) {
		t.Errorf("CaptureBody %q missing expected message", out.CaptureBody)
	}
}

func TestExecuteDispatchCollectBackgroundNotFound(t *testing.T) {
	// Execute dispatches to collectBackground, but the stream doesn't exist.
	exec := &GRPCExecutor{
		bgStreams: map[string]*backgroundStream{},
	}
	// No defer Close needed — bgStreams is empty, nothing to wait on.

	step := Step{
		GRPC: &GRPCRequest{
			Stream: &GRPCStream{
				ID: "missing-stream",
			},
		},
	}

	_, err := exec.Execute(context.Background(), step, nil)
	if !errors.Is(err, errGRPCStreamNotFound) {
		t.Errorf("error = %v, want %v", err, errGRPCStreamNotFound)
	}
}
