package attractor

import (
	"errors"
	"maps"
	"testing"
)

func TestParseFiles(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      map[string]string
		wantErr   error
		wantFiles int // expected number of files (0 means check wantErr)
	}{
		{
			name: "single file",
			input: `Some prose here
=== FILE: main.go ===
package main

func main() {}
=== END FILE ===
More prose`,
			want: map[string]string{
				"main.go": "package main\n\nfunc main() {}\n",
			},
			wantFiles: 1,
		},
		{
			name: "multiple files",
			input: `=== FILE: main.go ===
package main
=== END FILE ===
=== FILE: go.mod ===
module example
=== END FILE ===`,
			want: map[string]string{
				"main.go": "package main\n",
				"go.mod":  "module example\n",
			},
			wantFiles: 2,
		},
		{
			name: "nested path",
			input: `=== FILE: cmd/server/main.go ===
package main
=== END FILE ===`,
			want: map[string]string{
				"cmd/server/main.go": "package main\n",
			},
			wantFiles: 1,
		},
		{
			name:    "empty output",
			input:   "",
			wantErr: errNoFiles,
		},
		{
			name:    "no file blocks",
			input:   "just some text without any file markers",
			wantErr: errNoFiles,
		},
		{
			name: "unclosed block skipped",
			input: `=== FILE: main.go ===
package main
no end marker here`,
			wantErr: errNoFiles,
		},
		{
			name: "path traversal rejected",
			input: `=== FILE: ../etc/passwd ===
root:x:0:0
=== END FILE ===`,
			wantErr: errPathTraversal,
		},
		{
			name: "absolute path rejected",
			input: `=== FILE: /etc/passwd ===
root:x:0:0
=== END FILE ===`,
			wantErr: errPathTraversal,
		},
		{
			name: "trailing newline normalized",
			input: `=== FILE: main.go ===
package main


=== END FILE ===`,
			want: map[string]string{
				"main.go": "package main\n",
			},
			wantFiles: 1,
		},
		{
			name: "empty content gets single newline",
			input: `=== FILE: empty.txt ===
=== END FILE ===`,
			want: map[string]string{
				"empty.txt": "\n",
			},
			wantFiles: 1,
		},
		{
			name: "duplicate path last wins",
			input: `=== FILE: main.go ===
version 1
=== END FILE ===
=== FILE: main.go ===
version 2
=== END FILE ===`,
			want: map[string]string{
				"main.go": "version 2\n",
			},
			wantFiles: 1,
		},
		{
			name: "double dot in filename allowed",
			input: `=== FILE: archive..tar ===
content
=== END FILE ===`,
			want: map[string]string{
				"archive..tar": "content\n",
			},
			wantFiles: 1,
		},
		{
			name: "unchanged marker ignored",
			input: `=== UNCHANGED: keep.go ===
=== FILE: changed.go ===
new content
=== END FILE ===`,
			want: map[string]string{
				"changed.go": "new content\n",
			},
			wantFiles: 1,
		},
		{
			name: "unchanged marker inside open block preserved as content",
			input: `=== FILE: main.go ===
package main
=== UNCHANGED: other.go ===
func main() {}
=== END FILE ===`,
			want: map[string]string{
				"main.go": "package main\n=== UNCHANGED: other.go ===\nfunc main() {}\n",
			},
			wantFiles: 1,
		},
		{
			name: "unclosed block replaced by new block",
			input: `=== FILE: first.go ===
first content
=== FILE: second.go ===
second content
=== END FILE ===`,
			want: map[string]string{
				"second.go": "second content\n",
			},
			wantFiles: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseFiles(tt.input)

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(got) != tt.wantFiles {
				t.Fatalf("expected %d files, got %d: %v", tt.wantFiles, len(got), got)
			}

			for path, wantContent := range tt.want {
				gotContent, ok := got[path]
				if !ok {
					t.Errorf("missing file %q", path)
					continue
				}
				if gotContent != wantContent {
					t.Errorf("file %q:\n  got:  %q\n  want: %q", path, gotContent, wantContent)
				}
			}
		})
	}
}

func TestParseFilesWithMetadata(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantFiles   int
		wantDropped []string
		wantTrunc   bool
		wantErr     error
	}{
		{
			name: "all files closed",
			input: `=== FILE: main.go ===
package main
=== END FILE ===
=== FILE: go.mod ===
module app
=== END FILE ===`,
			wantFiles:   2,
			wantDropped: nil,
			wantTrunc:   false,
		},
		{
			name: "truncated mid-block",
			input: `=== FILE: main.go ===
package main
=== END FILE ===
=== FILE: handler.go ===
package main
func handle() {`,
			wantFiles:   1,
			wantDropped: []string{"handler.go"},
			wantTrunc:   true,
		},
		{
			name: "unclosed block replaced by new block",
			input: `=== FILE: first.go ===
first content
=== FILE: second.go ===
second content
=== END FILE ===`,
			wantFiles:   1,
			wantDropped: []string{"first.go"},
			wantTrunc:   false,
		},
		{
			name: "multiple dropped plus truncation",
			input: `=== FILE: a.go ===
aaa
=== FILE: b.go ===
bbb
=== END FILE ===
=== FILE: c.go ===
ccc
=== FILE: d.go ===
ddd`,
			wantFiles:   1,
			wantDropped: []string{"a.go", "c.go", "d.go"},
			wantTrunc:   true,
		},
		{
			name: "all unclosed returns error",
			input: `=== FILE: only.go ===
never closed`,
			wantErr: errNoFiles,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseFilesWithMetadata(tt.input)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(result.Files) != tt.wantFiles {
				t.Errorf("files: got %d, want %d", len(result.Files), tt.wantFiles)
			}
			if result.Truncated != tt.wantTrunc {
				t.Errorf("truncated: got %v, want %v", result.Truncated, tt.wantTrunc)
			}

			if len(result.DroppedFiles) != len(tt.wantDropped) {
				t.Fatalf("dropped: got %v, want %v", result.DroppedFiles, tt.wantDropped)
			}
			for i, want := range tt.wantDropped {
				if result.DroppedFiles[i] != want {
					t.Errorf("dropped[%d]: got %q, want %q", i, result.DroppedFiles[i], want)
				}
			}
		})
	}
}

func TestMergeFiles(t *testing.T) {
	tests := []struct {
		name      string
		newFiles  map[string]string
		prevFiles map[string]string
		want      map[string]string
	}{
		{
			name:      "new overrides prev",
			newFiles:  map[string]string{"main.go": "v2\n"},
			prevFiles: map[string]string{"main.go": "v1\n", "Dockerfile": "FROM scratch\n"},
			want:      map[string]string{"main.go": "v2\n", "Dockerfile": "FROM scratch\n"},
		},
		{
			name:      "carry forward unmodified",
			newFiles:  map[string]string{"handler.go": "new handler\n"},
			prevFiles: map[string]string{"main.go": "main\n", "go.mod": "module m\n"},
			want:      map[string]string{"handler.go": "new handler\n", "main.go": "main\n", "go.mod": "module m\n"},
		},
		{
			name:      "empty new carries all prev",
			newFiles:  map[string]string{},
			prevFiles: map[string]string{"main.go": "main\n"},
			want:      map[string]string{"main.go": "main\n"},
		},
		{
			name:      "empty prev returns new only",
			newFiles:  map[string]string{"main.go": "main\n"},
			prevFiles: map[string]string{},
			want:      map[string]string{"main.go": "main\n"},
		},
		{
			name:      "both empty",
			newFiles:  map[string]string{},
			prevFiles: map[string]string{},
			want:      map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Snapshot inputs to verify no mutation.
			origNew := maps.Clone(tt.newFiles)
			origPrev := maps.Clone(tt.prevFiles)

			got := MergeFiles(tt.newFiles, tt.prevFiles)

			if len(got) != len(tt.want) {
				t.Fatalf("expected %d files, got %d: %v", len(tt.want), len(got), got)
			}
			for path, wantContent := range tt.want {
				gotContent, ok := got[path]
				if !ok {
					t.Errorf("missing file %q", path)
					continue
				}
				if gotContent != wantContent {
					t.Errorf("file %q: got %q, want %q", path, gotContent, wantContent)
				}
			}

			// Verify inputs were not mutated.
			if !maps.Equal(tt.newFiles, origNew) {
				t.Error("newFiles was mutated")
			}
			if !maps.Equal(tt.prevFiles, origPrev) {
				t.Error("prevFiles was mutated")
			}
		})
	}
}
