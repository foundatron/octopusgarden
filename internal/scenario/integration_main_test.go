//go:build integration

package scenario

import (
	"os"
	"testing"
)

// TestMain runs before all tests in this package when building with -tags=integration.
// It ensures the shared Docker test service is torn down after all tests complete.
func TestMain(m *testing.M) {
	code := m.Run()
	teardownSharedService()
	os.Exit(code)
}
