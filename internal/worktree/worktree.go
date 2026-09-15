// Package worktree manages host git worktrees. It knows nothing about
// containers.
package worktree

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// git runs a git command in dir and returns its stdout. stderr is kept out of
// the return value and folded into the error instead: git writes warnings
// there while still exiting 0, and a caller that parses the combined stream
// reads those warnings as part of the answer.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
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

// RepoName names the repository dir belongs to, by the same rule saddlebag uses
// to resolve a project.
//
// saddlebag's resolve_project asks git for --git-common-dir and names the
// directory holding it, so a subdirectory of a repository and a linked
// worktree of it both resolve to the *main* repository's name. Deriving the
// name any other way - filepath.Base of the path handed to `saddle up`, say -
// would silently disagree with saddlebag for exactly those two cases, and a
// session that files its memories under a project name nobody queries is
// worse than one that files none: the writes succeed and never surface.
//
// A path that is not in a repository, or a machine with no usable git, falls
// back to the base name of dir. This never fails: a name saddle cannot
// improve on is not a reason to refuse to start a session.
func RepoName(ctx context.Context, dir string) string {
	out, err := git(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return filepath.Base(dir)
	}
	common := strings.TrimSpace(out)
	if common == "" {
		return filepath.Base(dir)
	}
	// git answers relative to the directory it ran in (plain ".git" at a
	// repository root), so resolve against dir before taking the parent.
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	return filepath.Base(filepath.Dir(filepath.Clean(common)))
}
