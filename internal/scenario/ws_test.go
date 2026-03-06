package scenario

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// newWSServer creates a test WebSocket server that:
// - echoes incoming messages prefixed with "echo: "
// - sends an initial message "hello" on connect if sendHello is true
func newWSServer(t *testing.T, sendHello bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()

		if sendHello {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"msg":"hello"}`)); err != nil {
				return
			}
		}

		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), websocket.MessageText, append([]byte("echo: "), data...)); err != nil {
				return
			}
		}
	}))
	return srv
}

func newWSExecutor(baseURL string) *WSExecutor {
	return &WSExecutor{
		BaseURL: baseURL,
		Logger:  newTestLogger(),
	}
}

func TestWSExecutorConnectSendReceive(t *testing.T) {
	srv := newWSServer(t, false)
	defer srv.Close()

	exec := newWSExecutor(srv.URL)
	defer exec.Close()

	step := Step{WS: &WSRequest{
		URL:     "/ws",
		Send:    "ping",
		Receive: &WSReceive{Timeout: "1s", Count: 1},
	}}

	out, err := exec.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out.Observed, "received 1 messages") {
		t.Errorf("unexpected observed: %s", out.Observed)
	}
	if !strings.Contains(out.CaptureBody, "echo: ping") {
		t.Errorf("unexpected capture body: %s", out.CaptureBody)
	}
}

func TestWSExecutorConnectionReuseByID(t *testing.T) {
	srv := newWSServer(t, false)
	defer srv.Close()

	exec := newWSExecutor(srv.URL)
	defer exec.Close()

	// Step 1: connect and send.
	step1 := Step{WS: &WSRequest{
		URL:  "/ws",
		ID:   "myconn",
		Send: "first",
	}}
	if _, err := exec.Execute(context.Background(), step1, nil); err != nil {
		t.Fatalf("step1 error: %v", err)
	}

	// Step 2: reuse connection by ID (no URL), receive echo from first send.
	step2 := Step{WS: &WSRequest{
		ID:      "myconn",
		Receive: &WSReceive{Timeout: "1s", Count: 1},
	}}
	out, err := exec.Execute(context.Background(), step2, nil)
	if err != nil {
		t.Fatalf("step2 error: %v", err)
	}
	if !strings.Contains(out.CaptureBody, "echo: first") {
		t.Errorf("expected echo, got: %s", out.CaptureBody)
	}
}

func TestWSExecutorMultipleConnections(t *testing.T) {
	srv := newWSServer(t, false)
	defer srv.Close()

	exec := newWSExecutor(srv.URL)
	defer exec.Close()

	stepA := Step{WS: &WSRequest{
		URL:     "/ws",
		ID:      "conn-a",
		Send:    "msg-a",
		Receive: &WSReceive{Timeout: "1s", Count: 1},
	}}
	stepB := Step{WS: &WSRequest{
		URL:     "/ws",
		ID:      "conn-b",
		Send:    "msg-b",
		Receive: &WSReceive{Timeout: "1s", Count: 1},
	}}

	outA, err := exec.Execute(context.Background(), stepA, nil)
	if err != nil {
		t.Fatalf("stepA error: %v", err)
	}
	outB, err := exec.Execute(context.Background(), stepB, nil)
	if err != nil {
		t.Fatalf("stepB error: %v", err)
	}

	if !strings.Contains(outA.CaptureBody, "msg-a") {
		t.Errorf("conn-a expected msg-a echo, got: %s", outA.CaptureBody)
	}
	if !strings.Contains(outB.CaptureBody, "msg-b") {
		t.Errorf("conn-b expected msg-b echo, got: %s", outB.CaptureBody)
	}
}

func TestWSExecutorReceiveTimeout(t *testing.T) {
	// Server that never sends anything.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		// Block until context done (test ends).
		<-r.Context().Done()
	}))
	defer srv.Close()

	exec := newWSExecutor(srv.URL)
	defer exec.Close()

	step := Step{WS: &WSRequest{
		URL:     "/ws",
		Receive: &WSReceive{Timeout: "50ms", Count: 3},
	}}

	out, err := exec.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error (timeout should return empty, not error): %v", err)
	}

	// Should return empty messages (timeout, not error).
	if strings.Contains(out.Observed, "received 3") {
		t.Errorf("should not have received 3 messages: %s", out.Observed)
	}
}

func TestWSExecutorReceiveCount(t *testing.T) {
	// Server that sends N messages on connect.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for i := range 5 {
			msg := strings.Repeat("x", i+1)
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(msg)); err != nil {
				return
			}
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	exec := newWSExecutor(srv.URL)
	defer exec.Close()

	step := Step{WS: &WSRequest{
		URL:     "/ws",
		Receive: &WSReceive{Timeout: "1s", Count: 3},
	}}

	out, err := exec.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.Observed, "received 3 messages") {
		t.Errorf("expected 3 messages, got: %s", out.Observed)
	}
}

func TestWSExecutorSendOnly(t *testing.T) {
	srv := newWSServer(t, false)
	defer srv.Close()

	exec := newWSExecutor(srv.URL)
	defer exec.Close()

	step := Step{WS: &WSRequest{
		URL:  "/ws",
		Send: "fire-and-forget",
		// No Receive.
	}}

	out, err := exec.Execute(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.Observed, "sent") {
		t.Errorf("expected sent in observed: %s", out.Observed)
	}
}

func TestWSExecutorReceiveOnly(t *testing.T) {
	srv := newWSServer(t, true) // sends "hello" on connect
	defer srv.Close()

	exec := newWSExecutor(srv.URL)
	defer exec.Close()

	// Connect first.
	step1 := Step{WS: &WSRequest{URL: "/ws"}}
	if _, err := exec.Execute(context.Background(), step1, nil); err != nil {
		t.Fatalf("connect error: %v", err)
	}

	// Receive the buffered "hello".
	step2 := Step{WS: &WSRequest{
		Receive: &WSReceive{Timeout: "1s", Count: 1},
	}}
	out, err := exec.Execute(context.Background(), step2, nil)
	if err != nil {
		t.Fatalf("receive error: %v", err)
	}
	if !strings.Contains(out.CaptureBody, "hello") {
		t.Errorf("expected hello in capture body: %s", out.CaptureBody)
	}
}

func TestWSExecutorVariableSubstitution(t *testing.T) {
	srv := newWSServer(t, false)
	defer srv.Close()

	exec := newWSExecutor(srv.URL)
	defer exec.Close()

	step := Step{WS: &WSRequest{
		URL:     "{path}",
		ID:      "{conn_id}",
		Send:    "hello {name}",
		Receive: &WSReceive{Timeout: "1s", Count: 1},
	}}

	vars := map[string]string{
		"path":    "/ws",
		"conn_id": "my-conn",
		"name":    "world",
	}

	out, err := exec.Execute(context.Background(), step, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.CaptureBody, "hello world") {
		t.Errorf("expected substituted message, got: %s", out.CaptureBody)
	}
}

func TestWSExecutorCloseCleanup(t *testing.T) {
	srv := newWSServer(t, false)
	defer srv.Close()

	exec := newWSExecutor(srv.URL)

	// Open two connections.
	for _, id := range []string{"c1", "c2"} {
		step := Step{WS: &WSRequest{URL: "/ws", ID: id}}
		if _, err := exec.Execute(context.Background(), step, nil); err != nil {
			t.Fatalf("connect %s: %v", id, err)
		}
	}

	// Close should not panic and should clean up.
	exec.Close()

	if exec.conns != nil {
		t.Error("expected conns to be nil after Close")
	}
}

func TestWSExecutorInvalidURL(t *testing.T) {
	exec := newWSExecutor("http://localhost:0")
	defer exec.Close()

	step := Step{WS: &WSRequest{
		URL: "/ws",
	}}

	_, err := exec.Execute(context.Background(), step, nil)
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestWSExecutorDefaultID(t *testing.T) {
	srv := newWSServer(t, false)
	defer srv.Close()

	exec := newWSExecutor(srv.URL)
	defer exec.Close()

	// Connect without specifying ID.
	step := Step{WS: &WSRequest{URL: "/ws", Send: "test"}}
	if _, err := exec.Execute(context.Background(), step, nil); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Should be accessible via "default" ID.
	if _, ok := exec.conns[defaultWSConnID]; !ok {
		t.Errorf("expected conn with id %q", defaultWSConnID)
	}
}

func TestWSExecutorNoConnectionNoURL(t *testing.T) {
	exec := newWSExecutor("http://localhost:8080")
	defer exec.Close()

	step := Step{WS: &WSRequest{
		Send:    "hello",
		Receive: &WSReceive{Timeout: "100ms", Count: 1},
	}}

	_, err := exec.Execute(context.Background(), step, nil)
	if err == nil {
		t.Fatal("expected error when no connection and no URL")
	}
	if !errors.Is(err, errWSConnNotFound) {
		t.Errorf("expected errWSConnNotFound, got: %v", err)
	}
}

func TestWSRunnerDispatch(t *testing.T) {
	srv := newWSServer(t, false)
	defer srv.Close()

	wsExec := newWSExecutor(srv.URL)
	runner := NewRunner(map[string]StepExecutor{
		"ws": wsExec,
	}, newTestLogger())

	sc := Scenario{
		ID: "ws-dispatch",
		Steps: []Step{
			{
				Description: "Send and receive",
				WS: &WSRequest{
					URL:     "/ws",
					Send:    "hi",
					Receive: &WSReceive{Timeout: "1s", Count: 1},
				},
				Expect: "Echo received",
			},
		},
	}

	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(result.Steps))
	}
	if result.Steps[0].Err != nil {
		t.Fatalf("step error: %v", result.Steps[0].Err)
	}
	if result.Steps[0].StepType != "ws" {
		t.Errorf("step type = %q, want ws", result.Steps[0].StepType)
	}
}
