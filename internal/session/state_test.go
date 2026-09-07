package session

import (
	"os"
	"testing"
	"time"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	in := State{
		Name: "saddle-demo", Profile: "go", Repo: "/repo",
		Worktree: "/wt", Branch: "saddle/demo", Network: "saddle-demo-net",
		Container: "saddle-demo", ProxyAddr: "192.168.128.1:9000",
		Status: "running", Created: time.Now().UTC().Truncate(time.Second),
	}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load("saddle-demo")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Worktree != in.Worktree || got.Status != "running" || !got.Created.Equal(in.Created) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, in)
	}
}

func TestListReturnsAllSessions(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, n := range []string{"a", "b"} {
		if err := Save(State{Name: n, Status: "running"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("List returned %d sessions, want 2", len(got))
	}
}

func TestDeleteRemovesSession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := Save(State{Name: "gone", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := Delete("gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("gone"); err == nil {
		t.Fatal("Load succeeded after Delete")
	}
}

// The case that actually breaks in the field: a container died behind
// saddle's back and the state file still says running.
func TestReconcileMarksDeadContainersStopped(t *testing.T) {
	in := []State{
		{Name: "alive", Container: "c1", Status: "running"},
		{Name: "dead", Container: "c2", Status: "running"},
	}
	got := Reconcile(in, map[string]bool{"c1": true})
	if got[0].Status != "running" {
		t.Errorf("alive session marked %q", got[0].Status)
	}
	if got[1].Status != "stopped" {
		t.Errorf("dead session marked %q, want stopped", got[1].Status)
	}
}

func TestReconcileDoesNotMutateInput(t *testing.T) {
	in := []State{{Name: "dead", Container: "c2", Status: "running"}}
	_ = Reconcile(in, map[string]bool{})
	if in[0].Status != "running" {
		t.Fatal("Reconcile mutated its input")
	}
}

func TestSaveRejectsNameWithPathSeparator(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := Save(State{Name: "../escape", Status: "running"}); err == nil {
		t.Fatal("Save succeeded with path traversal name")
	}
	// Verify no file was created outside the sessions dir
	d, _ := Dir()
	entries, _ := os.ReadDir(d)
	if len(entries) > 0 {
		t.Fatalf("File created with traversal name: %v", entries)
	}
}

func TestDeleteRejectsTraversalName(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := Delete("../../etc/passwd"); err == nil {
		t.Fatal("Delete succeeded with traversal name")
	}
}

func TestLoadRejectsTraversalName(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := Load("../escape"); err == nil {
		t.Fatal("Load succeeded with traversal name")
	}
}

func TestStateRoundTripsAnchorAllowlistAndSpawnedAddr(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	want := State{
		Name:      "acme-main",
		Anchor:    "acme-main-anchor",
		Network:   "acme-main-net",
		Container: "acme-main",
		ProxyAddr: "192.168.65.1:54321",
		Allow:     []string{"api.anthropic.com", "proxy.golang.org"},
		Spawned: []Spawned{{
			Name: "remem",
			PID:  4242,
			Cmd:  []string{"remem", "serve", "--http"},
			Addr: "192.168.65.1:9100",
		}},
	}
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := Load("acme-main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Anchor != want.Anchor {
		t.Errorf("Anchor = %q, want %q", got.Anchor, want.Anchor)
	}
	if len(got.Allow) != 2 || got.Allow[0] != "api.anthropic.com" || got.Allow[1] != "proxy.golang.org" {
		t.Errorf("Allow = %v, want %v", got.Allow, want.Allow)
	}
	if len(got.Spawned) != 1 || got.Spawned[0].Addr != "192.168.65.1:9100" {
		t.Errorf("Spawned = %+v", got.Spawned)
	}
}

// State files written before the anchor existed must still load: an operator
// mid-upgrade has sessions on disk, and a parse failure would strand them
// with no way to run `saddle down`.
func TestStateWithoutAnchorStillLoads(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := Save(State{Name: "old-session", Container: "old-session"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load("old-session")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Anchor != "" {
		t.Fatalf("Anchor = %q, want empty", got.Anchor)
	}
}
