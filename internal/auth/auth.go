// Package auth stores the container's Claude token in a saddle-owned keychain
// item, separate from the operator's own Claude credentials. A Linux container
// cannot reach the macOS keychain, so the token must be injected as an
// environment variable.
package auth

import (
	"fmt"
	"os/exec"
	"strings"
)

const service = "saddle-claude-token"

// Store writes or replaces the token. -U updates an existing item rather than
// failing on a duplicate.
func Store(token string) error {
	cmd := exec.Command("security", "add-generic-password",
		"-U", "-s", service, "-a", "saddle", "-w", token)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("store token: %w: %s", err, out)
	}
	return nil
}

// Load reads the token back.
func Load() (string, error) {
	out, err := exec.Command("security", "find-generic-password",
		"-s", service, "-w").Output()
	if err != nil {
		return "", fmt.Errorf("no saddle token found; run `saddle auth`: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
