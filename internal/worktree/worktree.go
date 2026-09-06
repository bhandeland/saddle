// Package worktree manages host git worktrees. It knows nothing about
// containers.
package worktree

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// Create adds a worktree at dest checked out on a new branch.
func Create(ctx context.Context, repo, branch, dest string) error {
	_, err := git(ctx, repo, "worktree", "add", "-b", branch, dest)
	return err
}

// Remove deletes the worktree at dest and prunes git's record of it.
func Remove(ctx context.Context, repo, dest string) error {
	_, err := git(ctx, repo, "worktree", "remove", "--force", dest)
	return err
}

// IsDirty reports whether the worktree has uncommitted or untracked changes.
func IsDirty(ctx context.Context, dest string) (bool, error) {
	out, err := git(ctx, dest, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}
