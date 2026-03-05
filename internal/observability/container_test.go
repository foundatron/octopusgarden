package observability

import (
	"context"
	"testing"
	"time"

	"github.com/foundatron/octopusgarden/internal/container"
)

type mockContainerMgr struct {
	buildFn        func(ctx context.Context, dir, tag string) error
	runFn          func(ctx context.Context, tag string) (string, container.StopFunc, error)
	runMultiPortFn func(ctx context.Context, tag string, extraPorts []string) (container.RunResult, container.StopFunc, error)
	waitHealthyFn  func(ctx context.Context, url string, timeout time.Duration) error
	waitPortFn     func(ctx context.Context, addr string, timeout time.Duration) error
	startSessionFn func(ctx context.Context, tag string) (*container.Session, container.StopFunc, error)
}

func (m *mockContainerMgr) Build(ctx context.Context, dir, tag string) error {
	return m.buildFn(ctx, dir, tag)
}

func (m *mockContainerMgr) Run(ctx context.Context, tag string) (string, container.StopFunc, error) {
	return m.runFn(ctx, tag)
}

func (m *mockContainerMgr) RunMultiPort(ctx context.Context, tag string, extraPorts []string) (container.RunResult, container.StopFunc, error) {
	if m.runMultiPortFn != nil {
		return m.runMultiPortFn(ctx, tag, extraPorts)
	}
	return container.RunResult{URL: "http://127.0.0.1:9999", ExtraPorts: map[string]string{}}, func() {}, nil
}

func (m *mockContainerMgr) WaitHealthy(ctx context.Context, url string, timeout time.Duration) error {
	return m.waitHealthyFn(ctx, url, timeout)
}

func (m *mockContainerMgr) WaitPort(ctx context.Context, addr string, timeout time.Duration) error {
	if m.waitPortFn != nil {
		return m.waitPortFn(ctx, addr, timeout)
	}
	return nil
}

func (m *mockContainerMgr) StartSession(ctx context.Context, tag string) (*container.Session, container.StopFunc, error) {
	return m.startSessionFn(ctx, tag)
}

func TestTracingContainerBuild(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{name: "success"},
		{name: "error", err: errMock, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp, tp := newTestTP()
			defer func() { _ = tp.Shutdown(context.Background()) }()

			mgr := &mockContainerMgr{
				buildFn: func(_ context.Context, _, _ string) error { return tt.err },
			}

			traced := NewTracingContainerManager(mgr, tp)
			err := traced.Build(context.Background(), "/tmp", "test:latest")

			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got %v", tt.wantErr, err)
			}

			_ = tp.ForceFlush(context.Background())
			spans := exp.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("expected 1 span, got %d", len(spans))
			}
			if spans[0].Name != "container.build" {
				t.Errorf("expected span name container.build, got %q", spans[0].Name)
			}
			assertHasAttr(t, spans[0].Attributes, "container.tag")
		})
	}
}

func TestTracingContainerRun(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		err     error
		wantErr bool
	}{
		{name: "success", url: "http://localhost:8080"},
		{name: "error", err: errMock, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp, tp := newTestTP()
			defer func() { _ = tp.Shutdown(context.Background()) }()

			mgr := &mockContainerMgr{
				runFn: func(_ context.Context, _ string) (string, container.StopFunc, error) {
					return tt.url, func() {}, tt.err
				},
			}

			traced := NewTracingContainerManager(mgr, tp)
			_, stop, err := traced.Run(context.Background(), "test:latest")

			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got %v", tt.wantErr, err)
			}

			// On success, span ends when stop is called (full container lifetime).
			if stop != nil {
				stop()
			}

			_ = tp.ForceFlush(context.Background())
			spans := exp.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("expected 1 span, got %d", len(spans))
			}
			if spans[0].Name != "container.run" {
				t.Errorf("expected span name container.run, got %q", spans[0].Name)
			}
		})
	}
}

func TestTracingContainerHealth(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{name: "success"},
		{name: "error", err: errMock, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp, tp := newTestTP()
			defer func() { _ = tp.Shutdown(context.Background()) }()

			mgr := &mockContainerMgr{
				waitHealthyFn: func(_ context.Context, _ string, _ time.Duration) error { return tt.err },
			}

			traced := NewTracingContainerManager(mgr, tp)
			err := traced.WaitHealthy(context.Background(), "http://localhost:8080", 5*time.Second)

			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got %v", tt.wantErr, err)
			}

			_ = tp.ForceFlush(context.Background())
			spans := exp.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("expected 1 span, got %d", len(spans))
			}
			if spans[0].Name != "container.health" {
				t.Errorf("expected span name container.health, got %q", spans[0].Name)
			}
		})
	}
}

func TestTracingContainerRunMultiPort(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		err     error
		wantErr bool
	}{
		{name: "success", url: "http://localhost:8080"},
		{name: "error", err: errMock, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp, tp := newTestTP()
			defer func() { _ = tp.Shutdown(context.Background()) }()

			mgr := &mockContainerMgr{
				runMultiPortFn: func(_ context.Context, _ string, _ []string) (container.RunResult, container.StopFunc, error) {
					return container.RunResult{
						URL:        tt.url,
						ExtraPorts: map[string]string{"50051/tcp": "127.0.0.1:50051"},
					}, func() {}, tt.err
				},
			}

			traced := NewTracingContainerManager(mgr, tp)
			_, stop, err := traced.RunMultiPort(context.Background(), "test:latest", []string{"50051/tcp"})

			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got %v", tt.wantErr, err)
			}

			// On success, span ends when stop is called (full container lifetime).
			if stop != nil {
				stop()
			}

			_ = tp.ForceFlush(context.Background())
			spans := exp.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("expected 1 span, got %d", len(spans))
			}
			if spans[0].Name != "container.run_multi_port" {
				t.Errorf("expected span name container.run_multi_port, got %q", spans[0].Name)
			}
			assertHasAttr(t, spans[0].Attributes, "container.tag")
			assertHasAttr(t, spans[0].Attributes, "container.extra_ports")
		})
	}
}

func TestTracingContainerWaitPort(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{name: "success"},
		{name: "error", err: errMock, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp, tp := newTestTP()
			defer func() { _ = tp.Shutdown(context.Background()) }()

			mgr := &mockContainerMgr{
				waitPortFn: func(_ context.Context, _ string, _ time.Duration) error { return tt.err },
			}

			traced := NewTracingContainerManager(mgr, tp)
			err := traced.WaitPort(context.Background(), "127.0.0.1:50051", 5*time.Second)

			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got %v", tt.wantErr, err)
			}

			_ = tp.ForceFlush(context.Background())
			spans := exp.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("expected 1 span, got %d", len(spans))
			}
			if spans[0].Name != "container.wait_port" {
				t.Errorf("expected span name container.wait_port, got %q", spans[0].Name)
			}
			assertHasAttr(t, spans[0].Attributes, "container.addr")
		})
	}
}

func TestTracingContainerSession(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{name: "success"},
		{name: "error", err: errMock, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp, tp := newTestTP()
			defer func() { _ = tp.Shutdown(context.Background()) }()

			mgr := &mockContainerMgr{
				startSessionFn: func(_ context.Context, _ string) (*container.Session, container.StopFunc, error) {
					return nil, func() {}, tt.err
				},
			}

			traced := NewTracingContainerManager(mgr, tp)
			_, stop, err := traced.StartSession(context.Background(), "test:latest")

			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got %v", tt.wantErr, err)
			}

			// On success, span ends when stop is called (full session lifetime).
			if stop != nil {
				stop()
			}

			_ = tp.ForceFlush(context.Background())
			spans := exp.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("expected 1 span, got %d", len(spans))
			}
			if spans[0].Name != "container.session" {
				t.Errorf("expected span name container.session, got %q", spans[0].Name)
			}
		})
	}
}
