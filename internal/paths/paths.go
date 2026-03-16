// Package paths resolves platform-native configuration and data paths for octog.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigDir returns the configuration directory for octog.
// It checks, in order:
//  1. OCTOG_CONFIG_DIR environment variable override
//  2. os.UserConfigDir()/octopusgarden (platform-native: ~/Library/Application Support on macOS, ~/.config on Linux)
//
// If the platform-native directory does not exist, it is returned so it can be
// created on demand.
func ConfigDir() (string, error) {
	if override := os.Getenv("OCTOG_CONFIG_DIR"); override != "" {
		return override, nil
	}

	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	return filepath.Join(base, "octopusgarden"), nil
}

// ConfigFile returns the path to the octog config file.
func ConfigFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config"), nil
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
