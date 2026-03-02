package container

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	dockerbuild "github.com/docker/docker/api/types/build"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockernetwork "github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var (
	errBuildFailed        = errors.New("container: image build failed")
	errNoPortBinding      = errors.New("container: no host port binding found for container port 8080/tcp")
	errContainerUnhealthy = errors.New("container: health check timed out")
	errNotADirectory      = errors.New("container: build context is not a directory")
)

// dockerAPI is the minimal Docker API surface used by Manager.
// Using an interface enables unit testing without a live Docker daemon.
type dockerAPI interface {
	ImageBuild(ctx context.Context, buildContext io.Reader, options dockerbuild.ImageBuildOptions) (dockerbuild.ImageBuildResponse, error)
	ContainerCreate(ctx context.Context, config *dockercontainer.Config, hostConfig *dockercontainer.HostConfig, networkingConfig *dockernetwork.NetworkingConfig, platform *ocispec.Platform, containerName string) (dockercontainer.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options dockercontainer.StartOptions) error
	ContainerInspect(ctx context.Context, containerID string) (dockercontainer.InspectResponse, error)
	ContainerStop(ctx context.Context, containerID string, options dockercontainer.StopOptions) error
	ContainerRemove(ctx context.Context, containerID string, options dockercontainer.RemoveOptions) error
}

// Compile-time check: *dockerclient.Client must implement dockerAPI.
var _ dockerAPI = (*dockerclient.Client)(nil)

// Manager builds Docker images and runs containers.
type Manager struct {
	docker dockerAPI
	http   *http.Client
	logger *slog.Logger
}

// StopFunc stops and removes a running container.
type StopFunc func()

// NewManager creates a Manager from Docker environment variables.
func NewManager(logger *slog.Logger) (*Manager, error) {
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("container: create docker client: %w", err)
	}
	return newManager(cli, &http.Client{Timeout: 5 * time.Second}, logger), nil
}

// newManager constructs a Manager with explicit dependencies (for testing).
func newManager(docker dockerAPI, httpClient *http.Client, logger *slog.Logger) *Manager {
	return &Manager{
		docker: docker,
		http:   httpClient,
		logger: logger,
	}
}

// Close closes the underlying Docker client if it implements io.Closer.
func (m *Manager) Close() error {
	if closer, ok := m.docker.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// Build builds a Docker image from the given directory with the given tag.
// The directory must contain a Dockerfile and all required build context files.
func (m *Manager) Build(ctx context.Context, dir, tag string) error {
	pr, err := tarDirectory(dir)
	if err != nil {
		return fmt.Errorf("container: tar directory: %w", err)
	}
	// Ensure the pipe reader is closed so the tar goroutine can exit
	// even if ImageBuild returns early without fully consuming the stream.
	defer func() { _ = pr.Close() }()

	resp, err := m.docker.ImageBuild(ctx, pr, dockerbuild.ImageBuildOptions{
		Tags:   []string{tag},
		Remove: true,
	})
	if err != nil {
		return fmt.Errorf("container: image build: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return parseBuildLog(resp.Body, m.logger)
}

// buildEvent is a single line from Docker's image build JSON stream.
type buildEvent struct {
	Stream string `json:"stream"`
	Error  string `json:"error"`
}

// parseBuildLog reads Docker's newline-delimited JSON build log, logging progress
// and returning an error (including the log) if the build fails.
func parseBuildLog(r io.Reader, logger *slog.Logger) error {
	var buildLog strings.Builder
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		var event buildEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			logger.Debug("docker build: unparseable line", "line", line, "err", err)
			continue
		}
		if event.Stream != "" {
			msg := strings.TrimRight(event.Stream, "\n")
			if msg != "" {
				logger.Debug("docker build", "line", msg)
				buildLog.WriteString(event.Stream)
			}
		}
		if event.Error != "" {
			return fmt.Errorf("%w: %s\nBuild log:\n%s", errBuildFailed, event.Error, buildLog.String())
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("container: read build log: %w", err)
	}
	return nil
}

// Run starts a container from the given image tag and returns the base URL and
// a stop function. The container must expose port 8080.
//
// Cleanup on partial failure:
//   - ContainerCreate fails  → return error (nothing to clean up)
//   - ContainerStart fails   → ContainerRemove(force) the created container
//   - ContainerInspect fails → ContainerStop + ContainerRemove
func (m *Manager) Run(ctx context.Context, tag string) (url string, stop StopFunc, err error) {
	const containerPort = nat.Port("8080/tcp")

	createResp, err := m.docker.ContainerCreate(ctx,
		&dockercontainer.Config{
			Image:        tag,
			ExposedPorts: nat.PortSet{containerPort: struct{}{}},
		},
		&dockercontainer.HostConfig{
			PortBindings: nat.PortMap{
				containerPort: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "0"}},
			},
		},
		nil, nil, "",
	)
	if err != nil {
		return "", nil, fmt.Errorf("container: create: %w", err)
	}
	containerID := createResp.ID

	if err := m.docker.ContainerStart(ctx, containerID, dockercontainer.StartOptions{}); err != nil {
		// Container was created but never started — remove only, no stop needed.
		_ = m.docker.ContainerRemove(context.Background(), containerID, dockercontainer.RemoveOptions{Force: true})
		return "", nil, fmt.Errorf("container: start: %w", err)
	}

	inspectResp, err := m.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		m.stopAndRemove(containerID)
		return "", nil, fmt.Errorf("container: inspect: %w", err)
	}

	bindings, ok := inspectResp.NetworkSettings.Ports[containerPort]
	if !ok || len(bindings) == 0 {
		m.stopAndRemove(containerID)
		return "", nil, errNoPortBinding
	}

	containerURL := "http://127.0.0.1:" + bindings[0].HostPort
	stopFn := func() { m.stopAndRemove(containerID) }

	shortID := containerID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	m.logger.Info("container started", "id", shortID, "url", containerURL)
	return containerURL, stopFn, nil
}

// stopAndRemove stops and forcibly removes a running container.
// Uses context.Background() so cleanup succeeds even after the caller's context is canceled.
func (m *Manager) stopAndRemove(containerID string) {
	ctx := context.Background()
	_ = m.docker.ContainerStop(ctx, containerID, dockercontainer.StopOptions{})
	_ = m.docker.ContainerRemove(ctx, containerID, dockercontainer.RemoveOptions{Force: true})
}

// WaitHealthy polls the container URL until it returns a non-5xx response or
// the timeout expires. Non-5xx responses (including 4xx) are considered healthy
// because they indicate the server is up; only 5xx indicates an unhealthy server.
// An immediate check is performed before the first ticker tick.
func (m *Manager) WaitHealthy(ctx context.Context, url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Check immediately before waiting for the first tick.
	if healthy, _ := m.checkHealth(ctx, url); healthy {
		return nil
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %s after %s", errContainerUnhealthy, url, timeout)
		case <-ticker.C:
			if healthy, _ := m.checkHealth(ctx, url); healthy {
				return nil
			}
		}
	}
}

// checkHealth returns true if the URL responds with a non-5xx HTTP status.
// Network errors are returned as (false, err) and callers should retry.
func (m *Manager) checkHealth(ctx context.Context, url string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	resp, err := m.http.Do(req) //nolint:gosec // G107: URL is controlled internal container port, not user input
	if err != nil {
		return false, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	return resp.StatusCode < 500, nil
}

// tarDirectory creates a streaming tar archive of the given directory.
// Uses io.Pipe to avoid buffering the entire build context in memory.
// Symlinks are skipped to prevent path traversal outside the build context.
// File paths are normalized to forward slashes for cross-platform Docker compatibility.
func tarDirectory(dir string) (*io.PipeReader, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("tar directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", errNotADirectory, dir)
	}

	pr, pw := io.Pipe()
	tw := tar.NewWriter(pw)
	go func() {
		err := filepath.WalkDir(dir, walkFn(dir, tw))
		if closeErr := tw.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		_ = pw.CloseWithError(err)
	}()
	return pr, nil
}

// walkFn returns an fs.WalkDirFunc that writes each non-symlink file in root to tw.
func walkFn(root string, tw *tar.Writer) fs.WalkDirFunc {
	return func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// Skip symlinks to prevent path traversal outside the build context.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("tar header for %s: %w", path, err)
		}
		// Make path relative to build context root and normalize separators.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", path, err)
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("write tar header for %s: %w", path, err)
		}
		if info.IsDir() {
			return nil
		}
		return copyFileToTar(path, tw)
	}
}

// copyFileToTar opens path and writes its contents to tw.
func copyFileToTar(path string, tw *tar.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("write tar body for %s: %w", path, err)
	}
	return nil
}
