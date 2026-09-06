package session

import (
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
