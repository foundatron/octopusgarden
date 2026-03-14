package attractor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/foundatron/octopusgarden/internal/llm"
)

func newTestHandler(t *testing.T) *agentToolHandler {
	t.Helper()
	h, err := newAgentToolHandler(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("newAgentToolHandler: %v", err)
	}
	return h
}

func toolCall(name string, input any) llm.ToolCall {
	raw, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	return llm.ToolCall{Name: name, Input: raw}
}

func TestAgentToolHandler(t *testing.T) {
	ctx := context.Background()

	t.Run("write_file basic", func(t *testing.T) {
		h := newTestHandler(t)
		result, err := h.Handle(ctx, toolCall("write_file", map[string]string{
			"path":    "main.go",
			"content": "package main\n",
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "ok" {
			t.Errorf("result = %q, want %q", result, "ok")
		}
		data, readErr := os.ReadFile(filepath.Join(h.iterDir, "main.go"))
		if readErr != nil {
			t.Fatalf("read file: %v", readErr)
		}
		if string(data) != "package main\n" {
			t.Errorf("file content = %q, want %q", string(data), "package main\n")
		}
	})

	t.Run("write_file nested", func(t *testing.T) {
		h := newTestHandler(t)
		_, err := h.Handle(ctx, toolCall("write_file", map[string]string{
			"path":    "src/main.go",
			"content": "package main\n",
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(h.iterDir, "src", "main.go")); statErr != nil {
			t.Errorf("file not created: %v", statErr)
		}
	})

	for _, tc := range []struct {
		name string
		path string
	}{
		{"traversal ../", "../escape.txt"},
		{"absolute path", "/etc/passwd"},
	} {
		t.Run("write_file "+tc.name, func(t *testing.T) {
			h := newTestHandler(t)
			_, err := h.Handle(ctx, toolCall("write_file", map[string]string{
				"path":    tc.path,
				"content": "bad",
			}))
			if !errors.Is(err, errPathTraversal) {
				t.Errorf("error = %v, want errPathTraversal", err)
			}
		})
	}

	t.Run("write_file empty content", func(t *testing.T) {
		h := newTestHandler(t)
		_, err := h.Handle(ctx, toolCall("write_file", map[string]string{
			"path":    "empty.go",
			"content": "",
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		info, statErr := os.Stat(filepath.Join(h.iterDir, "empty.go"))
		if statErr != nil {
			t.Fatalf("stat: %v", statErr)
		}
		if info.Size() != 0 {
			t.Errorf("size = %d, want 0", info.Size())
		}
	})

	t.Run("read_file basic", func(t *testing.T) {
		h := newTestHandler(t)
		if err := os.WriteFile(filepath.Join(h.iterDir, "hello.go"), []byte("package main\n"), 0o600); err != nil {
			t.Fatalf("setup: %v", err)
		}
		result, err := h.Handle(ctx, toolCall("read_file", map[string]string{"path": "hello.go"}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "package main\n" {
			t.Errorf("result = %q, want %q", result, "package main\n")
		}
	})

	t.Run("read_file nonexistent", func(t *testing.T) {
		h := newTestHandler(t)
		result, err := h.Handle(ctx, toolCall("read_file", map[string]string{"path": "missing.go"}))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if result == "" {
			t.Error("result should be non-empty error string")
		}
	})

	t.Run("read_file round-trip", func(t *testing.T) {
		h := newTestHandler(t)
		content := "package foo\n\nfunc Foo() {}\n"
		if _, err := h.Handle(ctx, toolCall("write_file", map[string]string{
			"path":    "foo.go",
			"content": content,
		})); err != nil {
			t.Fatalf("write: %v", err)
		}
		result, err := h.Handle(ctx, toolCall("read_file", map[string]string{"path": "foo.go"}))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if result != content {
			t.Errorf("round-trip mismatch: got %q, want %q", result, content)
		}
	})

	for _, tc := range []struct {
		name string
		path string
	}{
		{"traversal", "../../etc/passwd"},
		{"absolute", "/etc/passwd"},
	} {
		t.Run("read_file "+tc.name, func(t *testing.T) {
			h := newTestHandler(t)
			_, err := h.Handle(ctx, toolCall("read_file", map[string]string{"path": tc.path}))
			if !errors.Is(err, errPathTraversal) {
				t.Errorf("error = %v, want errPathTraversal", err)
			}
		})
	}

	t.Run("list_files empty dir", func(t *testing.T) {
		h := newTestHandler(t)
		result, err := h.Handle(ctx, toolCall("list_files", map[string]string{"directory": "."}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "" {
			t.Errorf("result = %q, want empty string", result)
		}
	})

	t.Run("list_files with files", func(t *testing.T) {
		h := newTestHandler(t)
		for _, name := range []string{"a.go", "b.go", "c.go"} {
			if _, err := h.Handle(ctx, toolCall("write_file", map[string]string{
				"path":    name,
				"content": "",
			})); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
		result, err := h.Handle(ctx, toolCall("list_files", map[string]string{"directory": "."}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		lines := nonEmptyLines(result)
		for _, name := range []string{"a.go", "b.go", "c.go"} {
			if !slices.Contains(lines, name) {
				t.Errorf("%s not found in output: %q", name, result)
			}
		}
	})

	t.Run("list_files subdirectory", func(t *testing.T) {
		h := newTestHandler(t)
		for _, p := range []string{"sub/a.go", "other/b.go"} {
			if _, err := h.Handle(ctx, toolCall("write_file", map[string]string{
				"path":    p,
				"content": "",
			})); err != nil {
				t.Fatalf("write %s: %v", p, err)
			}
		}
		result, err := h.Handle(ctx, toolCall("list_files", map[string]string{"directory": "sub"}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		lines := nonEmptyLines(result)
		if len(lines) != 1 || lines[0] != "a.go" {
			t.Errorf("result = %q, want only \"a.go\"", result)
		}
	})

	for _, tc := range []struct {
		name string
		dir  string
	}{
		{"traversal", "../"},
		{"absolute", "/etc"},
	} {
		t.Run("list_files "+tc.name, func(t *testing.T) {
			h := newTestHandler(t)
			_, err := h.Handle(ctx, toolCall("list_files", map[string]string{"directory": tc.dir}))
			if !errors.Is(err, errPathTraversal) {
				t.Errorf("error = %v, want errPathTraversal", err)
			}
		})
	}

	t.Run("list_files nonexistent dir", func(t *testing.T) {
		h := newTestHandler(t)
		result, err := h.Handle(ctx, toolCall("list_files", map[string]string{"directory": "nosuchdir"}))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if result == "" {
			t.Error("result should be non-empty error string")
		}
	})

	t.Run("unknown tool", func(t *testing.T) {
		h := newTestHandler(t)
		_, err := h.Handle(ctx, llm.ToolCall{Name: "delete_file", Input: json.RawMessage(`{}`)})
		if !errors.Is(err, errUnknownTool) {
			t.Errorf("error = %v, want errUnknownTool", err)
		}
	})

	t.Run("agentTools schema", func(t *testing.T) {
		tools := agentTools()
		if len(tools) != 3 {
			t.Fatalf("len(tools) = %d, want 3", len(tools))
		}
		for _, tool := range tools {
			var schema map[string]any
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				t.Errorf("tool %q: invalid JSON schema: %v", tool.Name, err)
			}
		}
	})

	t.Run("write_file bad JSON input", func(t *testing.T) {
		h := newTestHandler(t)
		_, err := h.Handle(ctx, llm.ToolCall{Name: "write_file", Input: json.RawMessage(`not json`)})
		if err == nil {
			t.Fatal("expected error for malformed JSON, got nil")
		}
	})
}

// nonEmptyLines splits s by newline and returns non-empty lines.
func nonEmptyLines(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
