//go:build integration

package container

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	dockertypes "github.com/docker/docker/api/types"
)

const integrationDockerfile = `FROM python:3-alpine
WORKDIR /srv
RUN echo "ok" > index.html
EXPOSE 8080
CMD ["python3", "-m", "http.server", "8080"]
`

func TestIntegrationBuildRunWaitHealthy(t *testing.T) {
	logger := newTestLogger()
	m, err := NewManager(logger)
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}
	defer func() { _ = m.Close() }()

	type pinger interface {
		Ping(context.Context) (dockertypes.Ping, error)
	}
	if p, ok := m.docker.(pinger); ok {
		if _, err := p.Ping(context.Background()); err != nil {
			t.Skipf("Docker daemon not available: %v", err)
		}
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(integrationDockerfile), 0o600); err != nil {
		t.Fatal(err)
	}

	tag := fmt.Sprintf("octopusgarden-integration-test:%d", time.Now().UnixNano())

	t.Logf("building image %s from %s", tag, dir)
	if err := m.Build(context.Background(), dir, tag); err != nil {
		t.Fatalf("Build: %v", err)
	}

	t.Logf("starting container")
	result, stop, err := m.Run(context.Background(), tag)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer stop()

	t.Logf("waiting for container at %s", result.URL)
	if err := m.WaitHealthy(context.Background(), result.URL, 30*time.Second); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}

	resp, err := http.Get(result.URL + "/") //nolint:gosec,noctx // integration test, URL is controlled local container
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 500 {
		t.Errorf("GET / returned status %d, want < 500", resp.StatusCode)
	}
	t.Logf("container responded with %d", resp.StatusCode)
}

func TestIntegrationRunTest(t *testing.T) {
	logger := newTestLogger()
	m, err := NewManager(logger)
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}
	defer func() { _ = m.Close() }()

	type pinger interface {
		Ping(context.Context) (dockertypes.Ping, error)
	}
	if p, ok := m.docker.(pinger); ok {
		if _, err := p.Ping(context.Background()); err != nil {
			t.Skipf("Docker daemon not available: %v", err)
		}
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(integrationDockerfile), 0o600); err != nil {
		t.Fatal(err)
	}

	tag := fmt.Sprintf("octopusgarden-integration-runtest:%d", time.Now().UnixNano())
	if err := m.Build(context.Background(), dir, tag); err != nil {
		t.Fatalf("Build: %v", err)
	}

	result, stop, err := m.Run(context.Background(), tag)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer stop()

	if err := m.WaitHealthy(context.Background(), result.URL, 30*time.Second); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}

	// Test command that exits 1 — verifies non-zero exit is captured.
	execResult, err := m.RunTest(context.Background(), result.ContainerID, "exit 1")
	if err != nil {
		t.Fatalf("RunTest: %v", err)
	}
	if execResult.ExitCode != 1 {
		t.Errorf("expected exit code 1, got %d", execResult.ExitCode)
	}
	t.Logf("RunTest exit code: %d", execResult.ExitCode)

	// Test command that exits 0 — verifies success path works.
	execResult, err = m.RunTest(context.Background(), result.ContainerID, "echo hello")
	if err != nil {
		t.Fatalf("RunTest echo: %v", err)
	}
	if execResult.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", execResult.ExitCode)
	}
}
