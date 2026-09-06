package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newRepo builds a throwaway git repo with one commit and returns its path.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestCreateMakesWorktreeOnNewBranch(t *testing.T) {
	repo := newRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	if err := Create(context.Background(), repo, "saddle/test", dest); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Fatalf("worktree missing tracked file: %v", err)
	}
}

func TestIsDirtyFalseThenTrue(t *testing.T) {
	repo := newRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	if err := Create(context.Background(), repo, "saddle/dirty", dest); err != nil {
		t.Fatal(err)
	}
	dirty, err := IsDirty(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Fatal("fresh worktree reported dirty")
	}
	if err := os.WriteFile(filepath.Join(dest, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err = IsDirty(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Fatal("worktree with untracked file reported clean")
	}
}

func TestRemoveDeletesWorktree(t *testing.T) {
	repo := newRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	if err := Create(context.Background(), repo, "saddle/gone", dest); err != nil {
		t.Fatal(err)
	}
	if err := Remove(context.Background(), repo, dest); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("worktree still present after Remove: %v", err)
	}
}
