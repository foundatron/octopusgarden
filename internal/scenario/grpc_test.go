package scenario

import (
	"testing"
	"time"
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
