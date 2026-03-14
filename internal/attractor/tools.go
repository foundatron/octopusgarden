package attractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/foundatron/octopusgarden/internal/llm"
)

var errUnknownTool = errors.New("attractor: unknown tool")

// agentToolHandler dispatches agent tool calls for file operations within an iteration directory.
// mu guards files against concurrent access in case AgentLoop implementations issue parallel tool
// calls. The current AnthropicClient.AgentLoop is sequential, but the ToolHandler contract does
// not guarantee single-threaded access.
type agentToolHandler struct {
	iterDir string
	logger  *slog.Logger
	mu      sync.Mutex
	files   map[string]string // tracks files written by write_file calls; guarded by mu
}

// newAgentToolHandler creates a handler rooted at iterDir.
// iterDir is resolved to an absolute path.
func newAgentToolHandler(iterDir string, logger *slog.Logger) (*agentToolHandler, error) {
	abs, err := filepath.Abs(iterDir)
	if err != nil {
		return nil, fmt.Errorf("attractor: resolve iterDir: %w", err)
	}
	return &agentToolHandler{
		iterDir: abs,
		logger:  logger,
		files:   make(map[string]string),
	}, nil
}

// Files returns a copy of the map of files written by the handler.
func (h *agentToolHandler) Files() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]string, len(h.files))
	maps.Copy(out, h.files)
	return out
}

type writeFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type readFileInput struct {
	Path string `json:"path"`
}

type listFilesInput struct {
	Directory string `json:"directory"`
}

// Handle dispatches a tool call to the appropriate handler.
func (h *agentToolHandler) Handle(_ context.Context, call llm.ToolCall) (string, error) {
	switch call.Name {
	case "write_file":
		return h.handleWriteFile(call.Input)
	case "read_file":
		return h.handleReadFile(call.Input)
	case "list_files":
		return h.handleListFiles(call.Input)
	default:
		return "", errUnknownTool
	}
}

func (h *agentToolHandler) handleWriteFile(raw json.RawMessage) (string, error) {
	var input writeFileInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("attractor: unmarshal write_file input: %w", err)
	}
	if err := validatePath(input.Path); err != nil {
		return "", err
	}
	if err := writeOneFile(h.iterDir, input.Path, input.Content); err != nil {
		return "", err
	}
	h.mu.Lock()
	h.files[input.Path] = input.Content
	h.mu.Unlock()
	if h.logger != nil {
		h.logger.Debug("agent wrote file", "path", input.Path)
	}
	return "ok", nil
}

// resolveContained joins rel to h.iterDir, resolves the absolute path, and verifies it remains
// within the sandbox. It mirrors the containment check in writeOneFile for consistency.
// Note: this does not resolve symlinks; a symlink to a file outside iterDir would pass.
// However, writeOneFile cannot create symlinks, so pre-existing symlinks are required to exploit.
func (h *agentToolHandler) resolveContained(rel string) (string, error) {
	abs, err := filepath.Abs(filepath.Join(h.iterDir, rel))
	if err != nil {
		return "", fmt.Errorf("attractor: resolve path: %w", err)
	}
	if abs != h.iterDir && !strings.HasPrefix(abs, h.iterDir+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s escapes workspace", errPathTraversal, rel)
	}
	return abs, nil
}

func (h *agentToolHandler) handleReadFile(raw json.RawMessage) (string, error) {
	var input readFileInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("attractor: unmarshal read_file input: %w", err)
	}
	if err := validatePath(input.Path); err != nil {
		return "", err
	}
	absPath, err := h.resolveContained(input.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		// File-not-found is returned as a string result so the agent can observe and adapt.
		// Other I/O errors (permission denied, disk failure) are returned as Go errors.
		if errors.Is(err, fs.ErrNotExist) {
			return err.Error(), nil
		}
		return "", fmt.Errorf("attractor: read_file %s: %w", input.Path, err)
	}
	return string(data), nil
}

func (h *agentToolHandler) handleListFiles(raw json.RawMessage) (string, error) {
	var input listFilesInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("attractor: unmarshal list_files input: %w", err)
	}
	dir := input.Directory
	if dir == "" {
		dir = "."
	}
	if err := validatePath(dir); err != nil {
		return "", err
	}
	root, err := h.resolveContained(dir)
	if err != nil {
		return "", err
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, entryErr error) error {
		if entryErr != nil {
			return entryErr
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		// Directory-not-found is returned as a string result so the agent can observe and adapt.
		if errors.Is(err, fs.ErrNotExist) {
			return err.Error(), nil
		}
		return "", fmt.Errorf("attractor: list_files walk: %w", err)
	}
	return strings.Join(paths, "\n"), nil
}

// agentTools returns the tool definitions available to the agent.
func agentTools() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name:        "write_file",
			Description: "Write content to a file in the workspace. Creates parent directories as needed.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative path of the file to write"},"content":{"type":"string","description":"Content to write to the file"}},"required":["path","content"]}`),
		},
		{
			Name:        "read_file",
			Description: "Read the content of a file in the workspace.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative path of the file to read"}},"required":["path"]}`),
		},
		{
			Name:        "list_files",
			Description: "List all files in a directory of the workspace. Returns newline-separated relative paths.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"directory":{"type":"string","description":"Relative path of the directory to list (default: \".\")"}}}`),
		},
	}
}
