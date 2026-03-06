//go:build integration

package scenario

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/foundatron/octopusgarden/internal/container"
)

// sharedService holds the Docker-based test service shared across integration tests.
type sharedService struct {
	baseURL    string // HTTP base URL, e.g. "http://127.0.0.1:12345"
	grpcTarget string // gRPC target address, e.g. "127.0.0.1:12346"
	imageTag   string // docker image tag (used for exec session tests)
	manager    *container.Manager
	stop       container.StopFunc
}

var (
	sharedServiceOnce sync.Once
	sharedSvc         *sharedService
	sharedSvcErr      error
)

// getSharedService returns the shared test service, starting it on first call.
// Tests that need Docker skip themselves if the service is unavailable.
func getSharedService(t *testing.T) *sharedService {
	t.Helper()
	sharedServiceOnce.Do(startSharedService)
	if sharedSvcErr != nil {
		t.Skipf("shared test service unavailable: %v", sharedSvcErr)
	}
	return sharedSvc
}

// teardownSharedService stops the shared container. Called from TestMain after m.Run().
func teardownSharedService() {
	if sharedSvc == nil {
		return
	}
	if sharedSvc.stop != nil {
		sharedSvc.stop()
	}
	if sharedSvc.manager != nil {
		_ = sharedSvc.manager.Close()
	}
}

// startSharedService compiles the testservice binary, packages it in a Docker image,
// and starts a container exposing HTTP (:8080) and gRPC (:9090).
func startSharedService() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mgr, err := container.NewManager(logger)
	if err != nil {
		sharedSvcErr = fmt.Errorf("create docker manager: %w", err)
		return
	}

	// Find the project root (two levels up from this source file's directory).
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		sharedSvcErr = fmt.Errorf("cannot determine source file path")
		_ = mgr.Close()
		return
	}
	projectRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../.."))

	// Compile the testservice binary for linux/GOARCH (static, no CGO).
	binDir, err := os.MkdirTemp("", "testservice-bin-*")
	if err != nil {
		sharedSvcErr = fmt.Errorf("create temp dir for binary: %w", err)
		_ = mgr.Close()
		return
	}
	defer func() { _ = os.RemoveAll(binDir) }()

	binPath := filepath.Join(binDir, "testservice")
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", binPath, "./internal/scenario/testservice")
	buildCmd.Dir = projectRoot
	buildCmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		sharedSvcErr = fmt.Errorf("compile testservice: %w\noutput:\n%s", err, out)
		_ = mgr.Close()
		return
	}

	// Build a minimal Docker context: the binary + a Dockerfile.
	ctxDir, err := os.MkdirTemp("", "testservice-docker-*")
	if err != nil {
		sharedSvcErr = fmt.Errorf("create docker context dir: %w", err)
		_ = mgr.Close()
		return
	}
	defer func() { _ = os.RemoveAll(ctxDir) }()

	if err := copyFile(binPath, filepath.Join(ctxDir, "testservice"), 0o755); err != nil {
		sharedSvcErr = fmt.Errorf("copy testservice binary: %w", err)
		_ = mgr.Close()
		return
	}

	const dockerfile = "FROM alpine:3.20\nCOPY testservice /testservice\nEXPOSE 8080 9090\nENTRYPOINT [\"/testservice\"]\n"
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		sharedSvcErr = fmt.Errorf("write Dockerfile: %w", err)
		_ = mgr.Close()
		return
	}

	tag := fmt.Sprintf("octopusgarden-testservice:%d", time.Now().UnixNano())
	if err := mgr.Build(ctx, ctxDir, tag); err != nil {
		sharedSvcErr = fmt.Errorf("docker build: %w", err)
		_ = mgr.Close()
		return
	}

	result, stop, err := mgr.RunMultiPort(ctx, tag, []string{"9090/tcp"})
	if err != nil {
		sharedSvcErr = fmt.Errorf("docker run: %w", err)
		_ = mgr.Close()
		return
	}

	grpcAddr, ok := result.ExtraPorts["9090/tcp"]
	if !ok {
		stop()
		sharedSvcErr = fmt.Errorf("gRPC port 9090 not bound")
		_ = mgr.Close()
		return
	}

	// Wait for HTTP health endpoint.
	if err := mgr.WaitHealthy(ctx, result.URL+"/healthz", 30*time.Second); err != nil {
		stop()
		sharedSvcErr = fmt.Errorf("wait HTTP healthy: %w", err)
		_ = mgr.Close()
		return
	}

	// Wait for gRPC port to accept connections.
	if err := mgr.WaitPort(ctx, grpcAddr, 30*time.Second); err != nil {
		stop()
		sharedSvcErr = fmt.Errorf("wait gRPC port: %w", err)
		_ = mgr.Close()
		return
	}

	sharedSvc = &sharedService{
		baseURL:    result.URL,
		grpcTarget: grpcAddr,
		imageTag:   tag,
		manager:    mgr,
		stop:       stop,
	}
}

// copyFile copies src to dst with the given permissions.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create dst: %w", err)
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return nil
}
