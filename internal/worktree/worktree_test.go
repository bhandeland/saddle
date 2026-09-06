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

// newNamedRepo builds a throwaway repo in a directory with a known name, so a
// test can assert on the name RepoName derives.
func newNamedRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
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

func TestRepoNameAtTheRepositoryRoot(t *testing.T) {
	repo := newNamedRepo(t, "acme-tools")
	if got := RepoName(context.Background(), repo); got != "acme-tools" {
		t.Fatalf("got %q want %q", got, "acme-tools")
	}
}

func TestRepoNameFromARelativePath(t *testing.T) {
	// `saddle up` with no argument passes ".". Up resolves it with
	// filepath.Abs first; this checks the pair produces the repository's real
	// name rather than "." - a project name no knowledge base ever queries.
	repo := newNamedRepo(t, "acme-tools")
	t.Chdir(repo)
	abs, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	if got := RepoName(context.Background(), abs); got != "acme-tools" {
		t.Fatalf("got %q want %q", got, "acme-tools")
	}
}

func TestRepoNameFromASubdirectoryNamesTheRepository(t *testing.T) {
	// remem's resolve_project uses --git-common-dir, so a subdirectory
	// resolves to the repository. filepath.Base would have said "session".
	repo := newNamedRepo(t, "acme-tools")
	sub := filepath.Join(repo, "internal", "session")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := RepoName(context.Background(), sub); got != "acme-tools" {
		t.Fatalf("got %q want %q", got, "acme-tools")
	}
}

func TestRepoNameInALinkedWorktreeNamesTheMainRepository(t *testing.T) {
	// A worktree shares the main repository's memories, because remem
	// resolves it to the common dir's parent.
	repo := newNamedRepo(t, "acme-tools")
	dest := filepath.Join(t.TempDir(), "some-feature")
	if err := Create(context.Background(), repo, "saddle/name", dest); err != nil {
		t.Fatal(err)
	}
	if got := RepoName(context.Background(), dest); got != "acme-tools" {
		t.Fatalf("got %q want %q", got, "acme-tools")
	}
}

func TestRepoNameFallsBackOutsideARepository(t *testing.T) {
	// Never a reason to fail `up`: name it after the directory and carry on.
	dir := filepath.Join(t.TempDir(), "loose-files")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := RepoName(context.Background(), dir); got != "loose-files" {
		t.Fatalf("got %q want %q", got, "loose-files")
	}
}
