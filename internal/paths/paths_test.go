package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigDir_Default(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("OCTOG_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "") // ensure os.UserConfigDir() uses $HOME, not a pre-set XDG dir

	got, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error: %v", err)
	}

	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("os.UserConfigDir() error: %v", err)
	}
	want := filepath.Join(base, "octopusgarden")
	if got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
}

func TestConfigDir_EnvOverride(t *testing.T) {
	override := t.TempDir()
	t.Setenv("OCTOG_CONFIG_DIR", override)

	got, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error: %v", err)
	}
	if got != override {
		t.Errorf("ConfigDir() = %q, want %q (override)", got, override)
	}
}

func TestConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OCTOG_CONFIG_DIR", dir)

	got, err := ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile() error: %v", err)
	}
	if filepath.Base(got) != "config" {
		t.Errorf("ConfigFile() base = %q, want config", filepath.Base(got))
	}
}

func TestDataDir_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OCTOG_CONFIG_DIR", dir)

	got, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir() error: %v", err)
	}
	if got != dir {
		t.Errorf("DataDir() = %q, want %q (OCTOG_CONFIG_DIR override)", got, dir)
	}
}

func TestDataDir_XDGDataHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OCTOG_CONFIG_DIR", "")
	t.Setenv("XDG_DATA_HOME", dir)

	got, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir() error: %v", err)
	}
	want := filepath.Join(dir, "octopusgarden")
	if got != want {
		t.Errorf("DataDir() = %q, want %q", got, want)
	}
}

func TestStorePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OCTOG_CONFIG_DIR", dir)

	got, err := StorePath()
	if err != nil {
		t.Fatalf("StorePath() error: %v", err)
	}
	if filepath.Base(got) != "runs.db" {
		t.Errorf("StorePath() base = %q, want runs.db", filepath.Base(got))
	}
}

func TestEnsureParentDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sub", "runs.db")

	if err := EnsureParentDir(target); err != nil {
		t.Fatalf("EnsureParentDir() error: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "sub"))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %04o, want 0700", perm)
	}
}
