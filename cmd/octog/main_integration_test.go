//go:build integration

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainHelpFlags(t *testing.T) {
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "octog")

	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	tests := []struct {
		args         []string
		wantExitCode int
		wantOutput   string
	}{
		{args: []string{"--help"}, wantExitCode: 0, wantOutput: "Usage: octog"},
		{args: []string{"-h"}, wantExitCode: 0, wantOutput: "Usage: octog"},
		{args: []string{"-help"}, wantExitCode: 0, wantOutput: "Usage: octog"},
		{args: []string{"run", "--help"}, wantExitCode: 0, wantOutput: "Usage: octog run"},
		{args: []string{"validate", "--help"}, wantExitCode: 0, wantOutput: "Usage: octog validate"},
		{args: []string{}, wantExitCode: 1, wantOutput: "Usage: octog"},
		{args: []string{"bogus"}, wantExitCode: 1, wantOutput: "unknown command"},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.args, "_"), func(t *testing.T) {
			cmd := exec.Command(bin, tt.args...)
			combined, _ := cmd.CombinedOutput()
			output := string(combined)

			exitCode := 0
			if cmd.ProcessState != nil && !cmd.ProcessState.Success() {
				exitCode = cmd.ProcessState.ExitCode()
			}

			if exitCode != tt.wantExitCode {
				t.Errorf("exit code = %d, want %d\noutput: %s", exitCode, tt.wantExitCode, output)
			}
			if tt.wantOutput != "" && !strings.Contains(output, tt.wantOutput) {
				t.Errorf("output does not contain %q\noutput: %s", tt.wantOutput, output)
			}
		})
	}
}
