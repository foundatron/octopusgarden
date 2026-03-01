package container

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dockerbuild "github.com/docker/docker/api/types/build"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// mockDockerAPI implements dockerAPI for unit tests.
type mockDockerAPI struct {
	imageBuildFunc       func(ctx context.Context, buildContext io.Reader, options dockerbuild.ImageBuildOptions) (dockerbuild.ImageBuildResponse, error)
	containerCreateFunc  func(ctx context.Context, config *dockercontainer.Config, hostConfig *dockercontainer.HostConfig, networkingConfig *dockernetwork.NetworkingConfig, platform *ocispec.Platform, containerName string) (dockercontainer.CreateResponse, error)
	containerStartFunc   func(ctx context.Context, containerID string, options dockercontainer.StartOptions) error
	containerInspectFunc func(ctx context.Context, containerID string) (dockercontainer.InspectResponse, error)
	containerStopFunc    func(ctx context.Context, containerID string, options dockercontainer.StopOptions) error
	containerRemoveFunc  func(ctx context.Context, containerID string, options dockercontainer.RemoveOptions) error
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
	url, stop, err := m.Run(context.Background(), "test:latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer stop()

	want := "http://127.0.0.1:49152"
	if url != want {
		t.Errorf("url = %q, want %q", url, want)
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
