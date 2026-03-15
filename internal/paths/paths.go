// Package paths resolves platform-native configuration and data paths for octog.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigDir returns the configuration directory for octog, plus an optional
// deprecation warning if the legacy ~/.octopusgarden path is being used.
// It checks, in order:
//  1. OCTOG_CONFIG_DIR environment variable override
//  2. os.UserConfigDir()/octopusgarden (platform-native: ~/Library/Application Support on macOS, ~/.config on Linux)
//  3. ~/.octopusgarden (legacy fallback — returns a non-empty deprecation warning)
//
// If neither the new nor legacy directory exists, the new platform-native location
// is returned so it can be created on demand.
func ConfigDir() (dir string, deprecationWarning string, err error) {
	if override := os.Getenv("OCTOG_CONFIG_DIR"); override != "" {
		return override, "", nil
	}

	base, err := os.UserConfigDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve config dir: %w", err)
	}
	newDir := filepath.Join(base, "octopusgarden")

	// If the new location already exists, use it.
	if _, err := os.Stat(newDir); err == nil {
		return newDir, "", nil
	}

	// Check for legacy ~/.octopusgarden fallback.
	home, err := os.UserHomeDir()
	if err != nil {
		// Can't find home dir — fall through to new location.
		return newDir, "", nil
	}
	legacyDir := filepath.Join(home, ".octopusgarden")
	if _, err := os.Stat(legacyDir); err == nil {
		warn := "config directory ~/.octopusgarden is deprecated; run `octog configure` to migrate to " + newDir
		return legacyDir, warn, nil
	}

	// Neither exists — return the new location (will be created on demand).
	return newDir, "", nil
}

// ConfigFile returns the path to the octog config file, plus an optional
// deprecation warning if the legacy ~/.octopusgarden path is being used.
func ConfigFile() (string, string, error) {
	dir, warn, err := ConfigDir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, "config"), warn, nil
}

// DataDir returns the data directory for octog (where the SQLite run-history database lives).
// It follows XDG Base Directory conventions, keeping application data separate from config:
//  1. OCTOG_CONFIG_DIR environment variable override (covers both config and data)
//  2. $XDG_DATA_HOME/octopusgarden when XDG_DATA_HOME is set
//  3. os.UserConfigDir()/octopusgarden as fallback (~/Library/Application Support on macOS,
//     ~/.config on Linux when XDG_DATA_HOME is unset)
func DataDir() (string, error) {
	if override := os.Getenv("OCTOG_CONFIG_DIR"); override != "" {
		return override, nil
	}
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		return filepath.Join(xdgData, "octopusgarden"), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve data dir: %w", err)
	}
	return filepath.Join(base, "octopusgarden"), nil
}

// StorePath returns the path to the SQLite run-history database.
// The database lives in DataDir, not ConfigDir, following XDG data/config separation.
func StorePath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runs.db"), nil
}

// EnsureParentDir creates the parent directory of path with 0700 permissions if it
// does not already exist. Permissions are 0700 (owner-only) — intentionally tighter
// than the legacy 0750 to prevent group read of potentially sensitive config and data.
func EnsureParentDir(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	return nil
}
