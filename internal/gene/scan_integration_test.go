//go:build integration

package gene

import (
	"context"
	"testing"

	"github.com/foundatron/octopusgarden/internal/testutil"
)

func TestScanSelfProject(t *testing.T) {
	dir := testutil.RepoRoot(t)

	res, err := Scan(context.Background(), dir, ScanOptions{})
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
