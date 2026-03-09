package container

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	dockerbuild "github.com/docker/docker/api/types/build"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerimage "github.com/docker/docker/api/types/image"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// mockDockerAPI implements dockerAPI for unit tests.
type mockDockerAPI struct {
	imageBuildFunc           func(ctx context.Context, buildContext io.Reader, options dockerbuild.ImageBuildOptions) (dockerbuild.ImageBuildResponse, error)
	containerCreateFunc      func(ctx context.Context, config *dockercontainer.Config, hostConfig *dockercontainer.HostConfig, networkingConfig *dockernetwork.NetworkingConfig, platform *ocispec.Platform, containerName string) (dockercontainer.CreateResponse, error)
	containerStartFunc       func(ctx context.Context, containerID string, options dockercontainer.StartOptions) error
	containerInspectFunc     func(ctx context.Context, containerID string) (dockercontainer.InspectResponse, error)
	containerStopFunc        func(ctx context.Context, containerID string, options dockercontainer.StopOptions) error
	containerRemoveFunc      func(ctx context.Context, containerID string, options dockercontainer.RemoveOptions) error
	containerExecCreateFunc  func(ctx context.Context, containerID string, config dockercontainer.ExecOptions) (dockercontainer.ExecCreateResponse, error)
	containerExecAttachFunc  func(ctx context.Context, execID string, config dockercontainer.ExecAttachOptions) (dockertypes.HijackedResponse, error)
	containerExecInspectFunc func(ctx context.Context, execID string) (dockercontainer.ExecInspect, error)
	imageRemoveFunc          func(ctx context.Context, imageID string, options dockerimage.RemoveOptions) ([]dockerimage.DeleteResponse, error)
}

func (m *mockDockerAPI) ImageBuild(ctx context.Context, buildContext io.Reader, options dockerbuild.ImageBuildOptions) (dockerbuild.ImageBuildResponse, error) {
	return m.imageBuildFunc(ctx, buildContext, options)
}

func (m *mockDockerAPI) ContainerCreate(ctx context.Context, config *dockercontainer.Config, hostConfig *dockercontainer.HostConfig, networkingConfig *dockernetwork.NetworkingConfig, platform *ocispec.Platform, containerName string) (dockercontainer.CreateResponse, error) {
	return m.containerCreateFunc(ctx, config, hostConfig, networkingConfig, platform, containerName)
}

func (m *mockDockerAPI) ContainerStart(ctx context.Context, containerID string, options dockercontainer.StartOptions) error {
	return m.containerStartFunc(ctx, containerID, options)
}

func (m *mockDockerAPI) ContainerInspect(ctx context.Context, containerID string) (dockercontainer.InspectResponse, error) {
	return m.containerInspectFunc(ctx, containerID)
}

func (m *mockDockerAPI) ContainerStop(ctx context.Context, containerID string, options dockercontainer.StopOptions) error {
	return m.containerStopFunc(ctx, containerID, options)
}

func (m *mockDockerAPI) ContainerRemove(ctx context.Context, containerID string, options dockercontainer.RemoveOptions) error {
	return m.containerRemoveFunc(ctx, containerID, options)
}

func (m *mockDockerAPI) ContainerExecCreate(ctx context.Context, containerID string, config dockercontainer.ExecOptions) (dockercontainer.ExecCreateResponse, error) {
	if m.containerExecCreateFunc != nil {
		return m.containerExecCreateFunc(ctx, containerID, config)
	}
	return dockercontainer.ExecCreateResponse{}, nil
}

func (m *mockDockerAPI) ContainerExecAttach(ctx context.Context, execID string, config dockercontainer.ExecAttachOptions) (dockertypes.HijackedResponse, error) {
	if m.containerExecAttachFunc != nil {
		return m.containerExecAttachFunc(ctx, execID, config)
	}
	return dockertypes.HijackedResponse{}, nil
}

func (m *mockDockerAPI) ContainerExecInspect(ctx context.Context, execID string) (dockercontainer.ExecInspect, error) {
	if m.containerExecInspectFunc != nil {
		return m.containerExecInspectFunc(ctx, execID)
	}
	return dockercontainer.ExecInspect{}, nil
}

func (m *mockDockerAPI) ImageRemove(ctx context.Context, imageID string, options dockerimage.RemoveOptions) ([]dockerimage.DeleteResponse, error) {
	if m.imageRemoveFunc != nil {
		return m.imageRemoveFunc(ctx, imageID, options)
	}
	return nil, nil
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// buildResponseBody constructs a newline-delimited JSON build event stream.
func buildResponseBody(events ...string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(strings.Join(events, "\n") + "\n"))
}

// inspectResponseWithPort creates an InspectResponse with a single port binding.
func inspectResponseWithPort(containerID, hostPort string) dockercontainer.InspectResponse {
	return dockercontainer.InspectResponse{
		ContainerJSONBase: &dockercontainer.ContainerJSONBase{ID: containerID},
		NetworkSettings: &dockercontainer.NetworkSettings{
			NetworkSettingsBase: dockercontainer.NetworkSettingsBase{ //nolint:staticcheck // SA1019: Ports field moves to NetworkSettings in v29; correct API for v28
				Ports: nat.PortMap{
					nat.Port("8080/tcp"): []nat.PortBinding{
						{HostIP: "127.0.0.1", HostPort: hostPort},
					},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Build tests
// ---------------------------------------------------------------------------

func TestBuildNonExistentDir(t *testing.T) {
	m := newManager(nil, nil, newTestLogger())
	err := m.Build(context.Background(), "/nonexistent/path", "test:latest")
	if err == nil {
		t.Fatal("expected error for non-existent directory")
	}
}

func TestBuildNotADirectory(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newManager(nil, nil, newTestLogger())
	err := m.Build(context.Background(), f, "test:latest")
	if !errors.Is(err, errNotADirectory) {
		t.Errorf("expected errNotADirectory, got: %v", err)
	}
}

func TestBuildSuccess(t *testing.T) {
	mock := &mockDockerAPI{
		imageBuildFunc: func(_ context.Context, _ io.Reader, _ dockerbuild.ImageBuildOptions) (dockerbuild.ImageBuildResponse, error) {
			body := buildResponseBody(
				`{"stream":"Step 1/2 : FROM alpine"}`,
				`{"stream":"Step 2/2 : CMD [\"/bin/sh\"]"}`,
				`{"stream":"Successfully built abc123"}`,
			)
			return dockerbuild.ImageBuildResponse{Body: body}, nil
		},
	}

	m := newManager(mock, nil, newTestLogger())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.Build(context.Background(), dir, "test:latest"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildDockerError(t *testing.T) {
	mock := &mockDockerAPI{
		imageBuildFunc: func(_ context.Context, _ io.Reader, _ dockerbuild.ImageBuildOptions) (dockerbuild.ImageBuildResponse, error) {
			body := buildResponseBody(
				`{"stream":"Step 1/2 : FROM alpine"}`,
				`{"error":"failed to solve: base name empty"}`,
			)
			return dockerbuild.ImageBuildResponse{Body: body}, nil
		},
	}

	m := newManager(mock, nil, newTestLogger())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := m.Build(context.Background(), dir, "test:latest")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errBuildFailed) {
		t.Errorf("expected errBuildFailed, got: %v", err)
	}
	if !strings.Contains(err.Error(), "failed to solve") {
		t.Errorf("error should contain the build error message, got: %v", err)
	}
}

func TestBuildAPIError(t *testing.T) {
	apiErr := errors.New("daemon unavailable")
	mock := &mockDockerAPI{
		imageBuildFunc: func(_ context.Context, _ io.Reader, _ dockerbuild.ImageBuildOptions) (dockerbuild.ImageBuildResponse, error) {
			return dockerbuild.ImageBuildResponse{}, apiErr
		},
	}

	m := newManager(mock, nil, newTestLogger())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := m.Build(context.Background(), dir, "test:latest")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, apiErr) {
		t.Errorf("expected wrapped apiErr, got: %v", err)
	}
}

func TestBuildTagPropagated(t *testing.T) {
	var gotOptions dockerbuild.ImageBuildOptions
	mock := &mockDockerAPI{
		imageBuildFunc: func(_ context.Context, _ io.Reader, options dockerbuild.ImageBuildOptions) (dockerbuild.ImageBuildResponse, error) {
			gotOptions = options
			return dockerbuild.ImageBuildResponse{Body: buildResponseBody(`{"stream":"ok"}`)}, nil
		},
	}

	m := newManager(mock, nil, newTestLogger())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine"), 0o600); err != nil {
		t.Fatal(err)
	}

	tag := "myapp:v1.2.3"
	if err := m.Build(context.Background(), dir, tag); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(gotOptions.Tags) != 1 || gotOptions.Tags[0] != tag {
		t.Errorf("Tags = %v, want [%q]", gotOptions.Tags, tag)
	}
}

func TestBuildTarIncludesFiles(t *testing.T) {
	var tarBytes []byte
	mock := &mockDockerAPI{
		imageBuildFunc: func(_ context.Context, buildContext io.Reader, _ dockerbuild.ImageBuildOptions) (dockerbuild.ImageBuildResponse, error) {
			var err error
			tarBytes, err = io.ReadAll(buildContext)
			if err != nil {
				return dockerbuild.ImageBuildResponse{}, err
			}
			return dockerbuild.ImageBuildResponse{Body: buildResponseBody(`{"stream":"ok"}`)}, nil
		},
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newManager(mock, nil, newTestLogger())
	if err := m.Build(context.Background(), dir, "test:latest"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Parse the captured tar and collect file names.
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	seen := make(map[string]bool)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		seen[hdr.Name] = true
	}

	for _, want := range []string{"Dockerfile", "main.go"} {
		if !seen[want] {
			t.Errorf("tar missing %q; entries: %v", want, seen)
		}
	}
}

// ---------------------------------------------------------------------------
// Run tests
// ---------------------------------------------------------------------------

const testContainerID = "abcdef123456789012"

func defaultRunMock(hostPort string) *mockDockerAPI {
	return &mockDockerAPI{
		containerCreateFunc: func(_ context.Context, _ *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig, _ *ocispec.Platform, _ string) (dockercontainer.CreateResponse, error) {
			return dockercontainer.CreateResponse{ID: testContainerID}, nil
		},
		containerStartFunc: func(_ context.Context, _ string, _ dockercontainer.StartOptions) error {
			return nil
		},
		containerInspectFunc: func(_ context.Context, _ string) (dockercontainer.InspectResponse, error) {
			return inspectResponseWithPort(testContainerID, hostPort), nil
		},
		containerStopFunc: func(_ context.Context, _ string, _ dockercontainer.StopOptions) error {
			return nil
		},
		containerRemoveFunc: func(_ context.Context, _ string, _ dockercontainer.RemoveOptions) error {
			return nil
		},
	}
}

func TestRunSuccess(t *testing.T) {
	m := newManager(defaultRunMock("49152"), nil, newTestLogger())
	result, stop, err := m.Run(context.Background(), "test:latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer stop()

	want := "http://127.0.0.1:49152"
	if result.URL != want {
		t.Errorf("url = %q, want %q", result.URL, want)
	}
	if result.ContainerID != testContainerID {
		t.Errorf("ContainerID = %q, want %q", result.ContainerID, testContainerID)
	}
}

func TestRunStopFnStopsAndRemoves(t *testing.T) {
	var stoppedID, removedID string
	mock := defaultRunMock("49152")
	mock.containerStopFunc = func(_ context.Context, containerID string, _ dockercontainer.StopOptions) error {
		stoppedID = containerID
		return nil
	}
	mock.containerRemoveFunc = func(_ context.Context, containerID string, _ dockercontainer.RemoveOptions) error {
		removedID = containerID
		return nil
	}

	m := newManager(mock, nil, newTestLogger())
	_, stop, err := m.Run(context.Background(), "test:latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stop()

	if stoppedID != testContainerID {
		t.Errorf("stop called with %q, want %q", stoppedID, testContainerID)
	}
	if removedID != testContainerID {
		t.Errorf("remove called with %q, want %q", removedID, testContainerID)
	}
}

func TestRunCreateError(t *testing.T) {
	createErr := errors.New("no such image")
	startCalled := false
	mock := &mockDockerAPI{
		containerCreateFunc: func(_ context.Context, _ *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig, _ *ocispec.Platform, _ string) (dockercontainer.CreateResponse, error) {
			return dockercontainer.CreateResponse{}, createErr
		},
		containerStartFunc: func(_ context.Context, _ string, _ dockercontainer.StartOptions) error {
			startCalled = true
			return nil
		},
	}

	m := newManager(mock, nil, newTestLogger())
	_, _, err := m.Run(context.Background(), "test:latest")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, createErr) {
		t.Errorf("expected wrapped createErr, got: %v", err)
	}
	if startCalled {
		t.Error("ContainerStart should not be called when create fails")
	}
}

func TestRunStartError(t *testing.T) {
	startErr := errors.New("cannot start")
	var removedID string
	var removeForced bool
	mock := &mockDockerAPI{
		containerCreateFunc: func(_ context.Context, _ *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig, _ *ocispec.Platform, _ string) (dockercontainer.CreateResponse, error) {
			return dockercontainer.CreateResponse{ID: testContainerID}, nil
		},
		containerStartFunc: func(_ context.Context, _ string, _ dockercontainer.StartOptions) error {
			return startErr
		},
		containerRemoveFunc: func(_ context.Context, containerID string, options dockercontainer.RemoveOptions) error {
			removedID = containerID
			removeForced = options.Force
			return nil
		},
	}

	m := newManager(mock, nil, newTestLogger())
	_, _, err := m.Run(context.Background(), "test:latest")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, startErr) {
		t.Errorf("expected wrapped startErr, got: %v", err)
	}
	if removedID != testContainerID {
		t.Errorf("remove called with %q, want %q", removedID, testContainerID)
	}
	if !removeForced {
		t.Error("ContainerRemove should use Force=true on cleanup")
	}
}

func TestRunNoPortBinding(t *testing.T) {
	mock := &mockDockerAPI{
		containerCreateFunc: func(_ context.Context, _ *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig, _ *ocispec.Platform, _ string) (dockercontainer.CreateResponse, error) {
			return dockercontainer.CreateResponse{ID: testContainerID}, nil
		},
		containerStartFunc: func(_ context.Context, _ string, _ dockercontainer.StartOptions) error {
			return nil
		},
		containerInspectFunc: func(_ context.Context, _ string) (dockercontainer.InspectResponse, error) {
			// Return inspect with empty ports map.
			return dockercontainer.InspectResponse{
				ContainerJSONBase: &dockercontainer.ContainerJSONBase{ID: testContainerID},
				NetworkSettings: &dockercontainer.NetworkSettings{
					NetworkSettingsBase: dockercontainer.NetworkSettingsBase{ //nolint:staticcheck // SA1019: Ports field moves to NetworkSettings in v29; correct API for v28
						Ports: nat.PortMap{},
					},
				},
			}, nil
		},
		containerStopFunc: func(_ context.Context, _ string, _ dockercontainer.StopOptions) error {
			return nil
		},
		containerRemoveFunc: func(_ context.Context, _ string, _ dockercontainer.RemoveOptions) error {
			return nil
		},
	}

	m := newManager(mock, nil, newTestLogger())
	_, _, err := m.Run(context.Background(), "test:latest")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errNoPortBinding) {
		t.Errorf("expected errNoPortBinding, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// WaitHealthy tests
// ---------------------------------------------------------------------------

func TestWaitHealthySuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := newManager(nil, srv.Client(), newTestLogger())
	if err := m.WaitHealthy(context.Background(), srv.URL+"/health", 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitHealthyTimeout(t *testing.T) {
	httpClient := &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       http.NoBody,
			}, nil
		}),
	}

	m := newManager(nil, httpClient, newTestLogger())
	err := m.WaitHealthy(context.Background(), "http://127.0.0.1:9999/health", 1500*time.Millisecond)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errContainerUnhealthy) {
		t.Errorf("expected errContainerUnhealthy, got: %v", err)
	}
}

func TestWaitHealthyContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// Simulate a server that is not yet healthy, so the immediate check fails
	// and the loop must fall through to the ctx.Done() case.
	httpClient := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			// Propagate context cancellation so the health check fails.
			return nil, r.Context().Err()
		}),
	}

	m := newManager(nil, httpClient, newTestLogger())
	err := m.WaitHealthy(ctx, "http://127.0.0.1:9999/health", 30*time.Second)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestWaitHealthyStatusCodes(t *testing.T) {
	tests := []struct {
		status  int
		healthy bool
	}{
		{http.StatusOK, true},
		{http.StatusMovedPermanently, true},
		{http.StatusNotFound, true},
		{http.StatusInternalServerError, false},
		{http.StatusServiceUnavailable, false},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("HTTP%d", tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			m := newManager(nil, srv.Client(), newTestLogger())
			healthy, _ := m.checkHealth(context.Background(), srv.URL+"/")
			if healthy != tc.healthy {
				t.Errorf("status %d: healthy = %v, want %v", tc.status, healthy, tc.healthy)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// StartSession tests
// ---------------------------------------------------------------------------

func defaultSessionMock() *mockDockerAPI {
	return &mockDockerAPI{
		containerCreateFunc: func(_ context.Context, _ *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig, _ *ocispec.Platform, _ string) (dockercontainer.CreateResponse, error) {
			return dockercontainer.CreateResponse{ID: testContainerID}, nil
		},
		containerStartFunc: func(_ context.Context, _ string, _ dockercontainer.StartOptions) error {
			return nil
		},
		containerStopFunc: func(_ context.Context, _ string, _ dockercontainer.StopOptions) error {
			return nil
		},
		containerRemoveFunc: func(_ context.Context, _ string, _ dockercontainer.RemoveOptions) error {
			return nil
		},
	}
}

func TestStartSessionSuccess(t *testing.T) {
	var gotConfig *dockercontainer.Config
	mock := defaultSessionMock()
	mock.containerCreateFunc = func(_ context.Context, config *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig, _ *ocispec.Platform, _ string) (dockercontainer.CreateResponse, error) {
		gotConfig = config
		return dockercontainer.CreateResponse{ID: testContainerID}, nil
	}

	m := newManager(mock, nil, newTestLogger())
	session, stop, err := m.StartSession(context.Background(), "test:latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer stop()

	if session == nil {
		t.Fatal("expected non-nil session")
	}
	// Verify entrypoint override and sleep infinity command
	if len(gotConfig.Entrypoint) != 1 || gotConfig.Entrypoint[0] != "sleep" {
		t.Errorf("expected entrypoint [sleep], got %v", gotConfig.Entrypoint)
	}
	if len(gotConfig.Cmd) != 1 || gotConfig.Cmd[0] != "infinity" {
		t.Errorf("expected cmd [infinity], got %v", gotConfig.Cmd)
	}
}

func TestStartSessionCreateError(t *testing.T) {
	mock := defaultSessionMock()
	mock.containerCreateFunc = func(_ context.Context, _ *dockercontainer.Config, _ *dockercontainer.HostConfig, _ *dockernetwork.NetworkingConfig, _ *ocispec.Platform, _ string) (dockercontainer.CreateResponse, error) {
		return dockercontainer.CreateResponse{}, errors.New("image not found")
	}

	m := newManager(mock, nil, newTestLogger())
	_, _, err := m.StartSession(context.Background(), "test:latest")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestStartSessionStartError(t *testing.T) {
	var removedID string
	mock := defaultSessionMock()
	mock.containerStartFunc = func(_ context.Context, _ string, _ dockercontainer.StartOptions) error {
		return errors.New("cannot start")
	}
	mock.containerRemoveFunc = func(_ context.Context, containerID string, _ dockercontainer.RemoveOptions) error {
		removedID = containerID
		return nil
	}

	m := newManager(mock, nil, newTestLogger())
	_, _, err := m.StartSession(context.Background(), "test:latest")
	if err == nil {
		t.Fatal("expected error")
	}
	if removedID != testContainerID {
		t.Errorf("expected container cleanup, got removeID=%q", removedID)
	}
}

func TestStartSessionStopCleansUp(t *testing.T) {
	var stoppedID, removedID string
	mock := defaultSessionMock()
	mock.containerStopFunc = func(_ context.Context, containerID string, _ dockercontainer.StopOptions) error {
		stoppedID = containerID
		return nil
	}
	mock.containerRemoveFunc = func(_ context.Context, containerID string, _ dockercontainer.RemoveOptions) error {
		removedID = containerID
		return nil
	}

	m := newManager(mock, nil, newTestLogger())
	_, stop, err := m.StartSession(context.Background(), "test:latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stop()

	if stoppedID != testContainerID {
		t.Errorf("expected stop with container ID, got %q", stoppedID)
	}
	if removedID != testContainerID {
		t.Errorf("expected remove with container ID, got %q", removedID)
	}
}

// ---------------------------------------------------------------------------
// Session.Exec tests
// ---------------------------------------------------------------------------

// buildMuxStream creates a stdcopy multiplexed stream with stdout and stderr content.
func buildMuxStream(stdout, stderr string) io.Reader {
	var buf bytes.Buffer
	stdoutWriter := stdcopy.NewStdWriter(&buf, stdcopy.Stdout)
	stderrWriter := stdcopy.NewStdWriter(&buf, stdcopy.Stderr)
	if stdout != "" {
		_, _ = stdoutWriter.Write([]byte(stdout))
	}
	if stderr != "" {
		_, _ = stderrWriter.Write([]byte(stderr))
	}
	return &buf
}

// fakeHijackedResponse creates a HijackedResponse from a reader for testing.
func fakeHijackedResponse(body io.Reader) dockertypes.HijackedResponse {
	// Create a pipe-based connection that the HijackedResponse can use.
	serverConn, clientConn := net.Pipe()
	go func() {
		// Write the body content to the server side so the client can read it.
		_, _ = io.Copy(serverConn, body)
		_ = serverConn.Close()
	}()
	return dockertypes.NewHijackedResponse(clientConn, "application/vnd.docker.raw-stream")
}

func TestSessionExecSuccess(t *testing.T) {
	mock := defaultSessionMock()
	mock.containerExecCreateFunc = func(_ context.Context, _ string, config dockercontainer.ExecOptions) (dockercontainer.ExecCreateResponse, error) {
		return dockercontainer.ExecCreateResponse{ID: "exec123"}, nil
	}
	mock.containerExecAttachFunc = func(_ context.Context, _ string, _ dockercontainer.ExecAttachOptions) (dockertypes.HijackedResponse, error) {
		return fakeHijackedResponse(buildMuxStream("hello\n", "")), nil
	}
	mock.containerExecInspectFunc = func(_ context.Context, _ string) (dockercontainer.ExecInspect, error) {
		return dockercontainer.ExecInspect{ExitCode: 0}, nil
	}

	session := &Session{containerID: testContainerID, docker: mock, logger: newTestLogger()}
	result, err := session.Exec(context.Background(), "echo hello", ExecOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "hello") {
		t.Errorf("expected 'hello' in stdout, got %q", result.Stdout)
	}
}

func TestSessionExecNonZeroExit(t *testing.T) {
	mock := defaultSessionMock()
	mock.containerExecCreateFunc = func(_ context.Context, _ string, _ dockercontainer.ExecOptions) (dockercontainer.ExecCreateResponse, error) {
		return dockercontainer.ExecCreateResponse{ID: "exec123"}, nil
	}
	mock.containerExecAttachFunc = func(_ context.Context, _ string, _ dockercontainer.ExecAttachOptions) (dockertypes.HijackedResponse, error) {
		return fakeHijackedResponse(buildMuxStream("", "error msg\n")), nil
	}
	mock.containerExecInspectFunc = func(_ context.Context, _ string) (dockercontainer.ExecInspect, error) {
		return dockercontainer.ExecInspect{ExitCode: 1}, nil
	}

	session := &Session{containerID: testContainerID, docker: mock, logger: newTestLogger()}
	result, err := session.Exec(context.Background(), "fail", ExecOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("expected exit code 1, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "error msg") {
		t.Errorf("expected 'error msg' in stderr, got %q", result.Stderr)
	}
}

func TestSessionExecCreateError(t *testing.T) {
	mock := defaultSessionMock()
	mock.containerExecCreateFunc = func(_ context.Context, _ string, _ dockercontainer.ExecOptions) (dockercontainer.ExecCreateResponse, error) {
		return dockercontainer.ExecCreateResponse{}, errors.New("container gone")
	}

	session := &Session{containerID: testContainerID, docker: mock, logger: newTestLogger()}
	_, err := session.Exec(context.Background(), "echo hi", ExecOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errExecFailed) {
		t.Errorf("expected errExecFailed, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RunTest tests
// ---------------------------------------------------------------------------

func TestRunTestSuccess(t *testing.T) {
	mock := defaultSessionMock()
	mock.containerExecCreateFunc = func(_ context.Context, _ string, _ dockercontainer.ExecOptions) (dockercontainer.ExecCreateResponse, error) {
		return dockercontainer.ExecCreateResponse{ID: "exec-test-123"}, nil
	}
	mock.containerExecAttachFunc = func(_ context.Context, _ string, _ dockercontainer.ExecAttachOptions) (dockertypes.HijackedResponse, error) {
		return fakeHijackedResponse(buildMuxStream("all tests passed\n", "")), nil
	}
	mock.containerExecInspectFunc = func(_ context.Context, _ string) (dockercontainer.ExecInspect, error) {
		return dockercontainer.ExecInspect{ExitCode: 0}, nil
	}

	m := newManager(mock, nil, newTestLogger())
	result, err := m.RunTest(context.Background(), testContainerID, "go test ./...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "all tests passed") {
		t.Errorf("expected stdout to contain 'all tests passed', got %q", result.Stdout)
	}
}

func TestRunTestNonZeroExit(t *testing.T) {
	mock := defaultSessionMock()
	mock.containerExecCreateFunc = func(_ context.Context, _ string, _ dockercontainer.ExecOptions) (dockercontainer.ExecCreateResponse, error) {
		return dockercontainer.ExecCreateResponse{ID: "exec-test-456"}, nil
	}
	mock.containerExecAttachFunc = func(_ context.Context, _ string, _ dockercontainer.ExecAttachOptions) (dockertypes.HijackedResponse, error) {
		return fakeHijackedResponse(buildMuxStream("", "FAIL: test_foo\n")), nil
	}
	mock.containerExecInspectFunc = func(_ context.Context, _ string) (dockercontainer.ExecInspect, error) {
		return dockercontainer.ExecInspect{ExitCode: 1}, nil
	}

	m := newManager(mock, nil, newTestLogger())
	result, err := m.RunTest(context.Background(), testContainerID, "go test ./...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "FAIL: test_foo") {
		t.Errorf("expected stderr to contain 'FAIL: test_foo', got %q", result.Stderr)
	}
}

func TestSessionExecPassesEnv(t *testing.T) {
	var gotConfig dockercontainer.ExecOptions
	mock := defaultSessionMock()
	mock.containerExecCreateFunc = func(_ context.Context, _ string, config dockercontainer.ExecOptions) (dockercontainer.ExecCreateResponse, error) {
		gotConfig = config
		return dockercontainer.ExecCreateResponse{ID: "exec123"}, nil
	}
	mock.containerExecAttachFunc = func(_ context.Context, _ string, _ dockercontainer.ExecAttachOptions) (dockertypes.HijackedResponse, error) {
		return fakeHijackedResponse(buildMuxStream("", "")), nil
	}
	mock.containerExecInspectFunc = func(_ context.Context, _ string) (dockercontainer.ExecInspect, error) {
		return dockercontainer.ExecInspect{ExitCode: 0}, nil
	}

	session := &Session{containerID: testContainerID, docker: mock, logger: newTestLogger()}
	_, err := session.Exec(context.Background(), "echo $FOO", ExecOptions{
		Env: map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	foundEnv := false
	for _, e := range gotConfig.Env {
		if e == "FOO=bar" {
			foundEnv = true
			break
		}
	}
	if !foundEnv {
		t.Errorf("expected FOO=bar in exec env, got %v", gotConfig.Env)
	}
}

// ---------------------------------------------------------------------------
// RemoveImage tests
// ---------------------------------------------------------------------------

func TestRemoveImage(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var gotImageID string
		var gotOptions dockerimage.RemoveOptions
		mock := &mockDockerAPI{
			imageRemoveFunc: func(_ context.Context, imageID string, options dockerimage.RemoveOptions) ([]dockerimage.DeleteResponse, error) {
				gotImageID = imageID
				gotOptions = options
				return nil, nil
			},
		}
		m := newManager(mock, nil, newTestLogger())
		m.RemoveImage(context.Background(), "test:latest")

		if gotImageID != "test:latest" {
			t.Errorf("ImageRemove called with %q, want %q", gotImageID, "test:latest")
		}
		if gotOptions.Force {
			t.Error("expected Force=false")
		}
		if !gotOptions.PruneChildren {
			t.Error("expected PruneChildren=true")
		}
	})

	t.Run("error_does_not_propagate", func(t *testing.T) {
		mock := &mockDockerAPI{
			imageRemoveFunc: func(_ context.Context, _ string, _ dockerimage.RemoveOptions) ([]dockerimage.DeleteResponse, error) {
				return nil, errors.New("image not found")
			},
		}
		m := newManager(mock, nil, newTestLogger())
		// Must not panic or propagate the error.
		m.RemoveImage(context.Background(), "test:latest")
	})
}
