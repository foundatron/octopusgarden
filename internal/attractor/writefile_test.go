package attractor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteOneFile(t *testing.T) {
	t.Run("simple file", func(t *testing.T) {
		baseDir := t.TempDir()
		content := "package main\n"
		if err := writeOneFile(baseDir, "main.go", content); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(baseDir, "main.go"))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != content {
			t.Errorf("content = %q, want %q", string(got), content)
		}
		info, err := os.Stat(filepath.Join(baseDir, "main.go"))
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %o, want 0o600", info.Mode().Perm())
		}
	})

	t.Run("deeply nested", func(t *testing.T) {
		baseDir := t.TempDir()
		if err := writeOneFile(baseDir, "a/b/c/d/file.go", "package d\n"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := os.Stat(filepath.Join(baseDir, "a", "b", "c", "d", "file.go")); err != nil {
			t.Errorf("file not created: %v", err)
		}
		// Parent directories must be created with 0o750.
		info, err := os.Stat(filepath.Join(baseDir, "a", "b"))
		if err != nil {
			t.Fatalf("stat parent dir: %v", err)
		}
		if info.Mode().Perm() != 0o750 {
			t.Errorf("dir mode = %o, want 0o750", info.Mode().Perm())
		}
	})

	for _, tc := range []struct {
		name string
		path string
	}{
		{"traversal ../", "../escape.txt"},
		{"deep traversal", "../../../etc/passwd"},
		{"dot-dot escapes via middle", "a/../../escape.txt"},
		// "." and "" both resolve to absDir itself (no separator prefix) and are rejected.
		{"dot path", "."},
		{"empty path", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseDir := t.TempDir()
			err := writeOneFile(baseDir, tc.path, "bad")
			if !errors.Is(err, errPathTraversal) {
				t.Errorf("error = %v, want errPathTraversal", err)
			}
		})
	}

	// Absolute paths are absorbed by filepath.Join: filepath.Join(absDir, "/etc/passwd")
	// = absDir+"/etc/passwd", which is contained. validatePath (called at the tool handler
	// layer) catches absolute paths before they reach writeOneFile, so writeOneFile itself
	// does not need to reject them.
	t.Run("absolute path absorbed by Join", func(t *testing.T) {
		baseDir := t.TempDir()
		// /etc/passwd written via writeOneFile goes to baseDir/etc/passwd, not /etc/passwd.
		if err := writeOneFile(baseDir, "/etc/passwd", "not the real one"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := os.Stat(filepath.Join(baseDir, "etc", "passwd")); err != nil {
			t.Errorf("expected file at baseDir/etc/passwd: %v", err)
		}
	})

	t.Run("dot-dot cleans to inside", func(t *testing.T) {
		// a/b/../../c.go resolves to <base>/c.go — still contained.
		baseDir := t.TempDir()
		if err := writeOneFile(baseDir, "a/b/../../c.go", "package main\n"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := os.Stat(filepath.Join(baseDir, "c.go")); err != nil {
			t.Errorf("c.go not created: %v", err)
		}
	})

	t.Run("prefix attack", func(t *testing.T) {
		// Without the separator in the HasPrefix check, a sibling directory whose name
		// shares the base dir's name as a prefix would pass (e.g. /tmp/foo-evil passes
		// HasPrefix(/tmp/foo) but not HasPrefix(/tmp/foo/)).
		parent := t.TempDir()
		baseDir := filepath.Join(parent, "base")
		if err := os.MkdirAll(baseDir, 0o750); err != nil {
			t.Fatalf("setup: %v", err)
		}
		// ../base-evil/file.go resolves to parent/base-evil/file.go.
		// HasPrefix(parent/base-evil/file.go, parent/base/) must be false.
		err := writeOneFile(baseDir, "../base-evil/file.go", "bad")
		if !errors.Is(err, errPathTraversal) {
			t.Errorf("error = %v, want errPathTraversal", err)
		}
	})

	t.Run("symlink escape (known gap)", func(t *testing.T) {
		// writeOneFile uses filepath.Abs, not filepath.EvalSymlinks.
		// A pre-existing symlink inside the workspace pointing outside the sandbox
		// passes the containment check. This is a known limitation: exploitation
		// requires attacker-controlled pre-existing symlinks in the workspace.
		baseDir := t.TempDir()
		outside := t.TempDir()
		symlinkPath := filepath.Join(baseDir, "link")
		if err := os.Symlink(outside, symlinkPath); err != nil {
			t.Skip("cannot create symlink:", err)
		}
		t.Log("symlink escape gap: writeOneFile does not call EvalSymlinks; a symlink inside the workspace can point outside the sandbox")
		err := writeOneFile(baseDir, "link/injected.txt", "content via symlink")
		if errors.Is(err, errPathTraversal) {
			t.Log("symlink was unexpectedly blocked (EvalSymlinks may now be in use)")
		}
		// No hard assertion: we document the gap rather than enforce its presence.
	})
}
