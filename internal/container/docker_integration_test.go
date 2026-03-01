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
		t.Fatalf("NewManager: %v", err)
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
	url, stop, err := m.Run(context.Background(), tag)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer stop()

	t.Logf("waiting for container at %s", url)
	if err := m.WaitHealthy(context.Background(), url, 30*time.Second); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}

	resp, err := http.Get(url + "/") //nolint:gosec,noctx // integration test, URL is controlled local container
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 500 {
		t.Errorf("GET / returned status %d, want < 500", resp.StatusCode)
	}
	t.Logf("container responded with %d", resp.StatusCode)
}
