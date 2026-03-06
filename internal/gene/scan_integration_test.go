//go:build integration

package gene

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScanSelfProject(t *testing.T) {
	// Find repo root by walking up from this file's directory.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root with go.mod")
		}
		dir = parent
	}

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if res.Language != "go" {
		t.Errorf("Language = %q, want %q", res.Language, "go")
	}
	if findRole(res.Files, "marker") == nil {
		t.Error("expected at least a marker file")
	}
	if findRole(res.Files, "entrypoint") == nil {
		t.Error("expected at least an entrypoint file")
	}
}
