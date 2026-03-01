package attractor

import (
	"errors"
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
