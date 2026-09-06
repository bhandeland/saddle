//go:build contract

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLsRunsOnACleanMachine is a smoke test of the CLI's wiring: it builds
// the real binary and confirms `saddle ls` runs cleanly against a machine
// with no saddle state and prints the expected header. It is not a full
// round trip — a genuine `up`/`down` cycle needs a real Claude OAuth token
// and live containers, neither of which is available in an unattended run,
// so that coverage is verified manually instead. See
// docs/MANUAL-VERIFICATION.md for the checklist.
func TestLsRunsOnACleanMachine(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "saddle")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "T"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module e2e\n"), 0o644)
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.CombinedOutput()
	}

	out, err := exec.Command(bin, "ls").CombinedOutput()
	if err != nil {
		t.Fatalf("saddle ls failed on a clean machine: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "NAME") {
		t.Fatalf("ls did not print a header: %s", out)
	}
}
