package scenario

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	defaultWSReceiveTimeout = 5 * time.Second
	defaultWSConnID         = "default"
	wsMessageBufferCap      = 1000
)

var errWSConnNotFound = errors.New("ws connection not found for id")

// WSExecutor executes WebSocket steps.
// Each WSExecutor instance is per-scenario (not shared across concurrent runs).
// WSExecutor is NOT safe for concurrent use from multiple goroutines.
type WSExecutor struct {
	BaseURL string
	Logger  *slog.Logger

	conns map[string]*wsConn
}

// wsConn wraps a WebSocket connection with a buffered message reader goroutine.
type wsConn struct {
	conn     *websocket.Conn
	mu       sync.Mutex
	messages []string
	readErr  error
	cancel   context.CancelFunc
	done     chan struct{}
}

// ValidCaptureSources returns nil — WS steps use JSONPath on CaptureBody only.
func (e *WSExecutor) ValidCaptureSources() []string { return nil }

// Execute dispatches a WebSocket step: connect (if needed), send, and/or receive.
func (e *WSExecutor) Execute(ctx context.Context, step Step, vars map[string]string) (StepOutput, error) {
	req := substituteWSRequest(*step.WS, vars)

	connID := req.ID
	if connID == "" {
		connID = defaultWSConnID
	}

	// Connect if URL is provided.
	if req.URL != "" {
		wsURL, err := e.buildWSURL(req.URL)
		if err != nil {
			return StepOutput{}, fmt.Errorf("ws: build url: %w", err)
		}
		if err := e.connect(ctx, connID, wsURL); err != nil {
			return StepOutput{}, fmt.Errorf("ws: connect %s: %w", wsURL, err)
		}
	}

	conn, ok := e.getConn(connID)
	if !ok {
		return StepOutput{}, fmt.Errorf("ws: connection %q: %w", connID, errWSConnNotFound)
	}

	// Send message if provided.
	if req.Send != "" {
		if err := conn.conn.Write(ctx, websocket.MessageText, []byte(req.Send)); err != nil {
			return StepOutput{}, fmt.Errorf("ws: write to %q: %w", connID, err)
		}
	}

	// Receive messages if configured.
	if req.Receive == nil {
		// No receive configured — send-only or connect-only step.
		var observed string
		if req.Send != "" {
			observed = fmt.Sprintf("WebSocket [%s]: sent %d bytes", connID, len(req.Send))
		} else {
			observed = fmt.Sprintf("WebSocket [%s]: connected", connID)
		}
		return StepOutput{Observed: observed, CaptureBody: "null"}, nil
	}

	messages, err := e.receiveMessages(conn, req.Receive)
	if err != nil {
		return StepOutput{}, fmt.Errorf("ws: receive from %q: %w", connID, err)
	}

	return buildWSOutput(connID, req.Send, messages), nil
}

// Close closes all WebSocket connections and cancels background readers.
// Safe to call on a never-initialized executor.
func (e *WSExecutor) Close() {
	for _, c := range e.conns {
		// Close the connection first so the background reader's conn.Read() returns,
		// then cancel and wait. Canceling first races with conn.Close().
		_ = c.conn.Close(websocket.StatusNormalClosure, "")
		c.cancel()
		<-c.done
	}
	e.conns = nil
}

func (e *WSExecutor) connect(ctx context.Context, connID, wsURL string) error {
	if e.conns == nil {
		e.conns = make(map[string]*wsConn)
	}

	// Close existing connection with same ID before reconnecting.
	if existing, ok := e.conns[connID]; ok {
		// Close the connection first so the background reader returns cleanly.
		_ = existing.conn.Close(websocket.StatusNormalClosure, "")
		existing.cancel()
		<-existing.done
		delete(e.conns, connID)
	}

	conn, _, err := websocket.Dial(ctx, wsURL, nil) //nolint:bodyclose // websocket.Dial manages its own response body
	if err != nil {
		return fmt.Errorf("dial %s: %w", wsURL, err)
	}
	conn.SetReadLimit(10 * 1024 * 1024) // 10 MB

	bgCtx, cancel := context.WithCancel(context.Background()) //nolint:containedctx // background reader needs a long-lived context
	c := &wsConn{
		conn:   conn,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	e.conns[connID] = c

	go func() {
		defer close(c.done)
		for {
			_, data, err := conn.Read(bgCtx)
			if err != nil {
				c.mu.Lock()
				c.readErr = err
				c.mu.Unlock()
				return
			}
			c.mu.Lock()
			if len(c.messages) < wsMessageBufferCap {
				c.messages = append(c.messages, string(data))
			}
			c.mu.Unlock()
		}
	}()

	return nil
}

func (e *WSExecutor) getConn(connID string) (*wsConn, bool) {
	if e.conns == nil {
		return nil, false
	}
	c, ok := e.conns[connID]
	return c, ok
}

func (e *WSExecutor) receiveMessages(c *wsConn, recv *WSReceive) ([]string, error) {
	recvTimeout, err := parseStepTimeout(recv.Timeout, defaultWSReceiveTimeout)
	if err != nil {
		e.Logger.Warn("ws: invalid receive timeout, using default",
			"timeout", recv.Timeout,
			"default", defaultWSReceiveTimeout,
			"error", err,
		)
		recvTimeout = defaultWSReceiveTimeout
	}

	count := recv.Count
	if count <= 0 {
		count = 1
	}

	timer := time.NewTimer(recvTimeout)
	defer timer.Stop()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	messages := make([]string, 0, count)
loop:
	for len(messages) < count {
		c.mu.Lock()
		available := len(c.messages)
		if available > 0 {
			take := min(available, count-len(messages))
			taken := make([]string, take)
			copy(taken, c.messages[:take])
			messages = append(messages, taken...)
			c.messages = c.messages[take:]
		}
		readErr := c.readErr
		c.mu.Unlock()

		if len(messages) >= count {
			break
		}
		if readErr != nil {
			break
		}

		select {
		case <-timer.C:
			break loop
		case <-ticker.C:
		}
	}

	return messages, nil
}

func (e *WSExecutor) buildWSURL(path string) (string, error) {
	base := e.BaseURL

	// Convert http → ws, https → wss. Match longer prefix first.
	base = strings.NewReplacer("https://", "wss://", "http://", "ws://").Replace(base)

	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}

	u.Path = path
	return u.String(), nil
}

func substituteWSRequest(req WSRequest, vars map[string]string) WSRequest {
	return WSRequest{
		URL:     substituteVars(req.URL, vars),
		ID:      substituteVars(req.ID, vars),
		Send:    substituteVars(req.Send, vars),
		Receive: req.Receive,
	}
}

func buildWSOutput(connID, sent string, messages []string) StepOutput {
	var observed strings.Builder
	fmt.Fprintf(&observed, "WebSocket [%s]", connID)
	if sent != "" {
		fmt.Fprintf(&observed, ": sent %d bytes", len(sent))
	}
	fmt.Fprintf(&observed, ": received %d messages", len(messages))
	if len(messages) > 0 {
		fmt.Fprintf(&observed, "\n%s", strings.Join(messages, "\n"))
	}

	// CaptureBody: JSON array of messages for JSONPath extraction.
	// When there are no messages (e.g. timeout with 0 received), return "[]" rather than
	// leaving captureBody empty, which would break downstream JSONPath extraction.
	// Note: individual messages are used as-is; if a message is not valid JSON, the
	// resulting array form ("[msg1,msg2]") will be malformed JSON — this matches the
	// single-message case where the raw string is also passed through unchanged.
	var captureBody string
	switch {
	case len(messages) == 0:
		captureBody = "[]"
	case len(messages) == 1:
		captureBody = messages[0]
	default:
		captureBody = "[" + strings.Join(messages, ",") + "]"
	}

	return StepOutput{
		Observed:    observed.String(),
		CaptureBody: captureBody,
	}
}
