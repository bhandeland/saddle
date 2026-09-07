package session

import "testing"

func TestAnchorNameDerivesFromTheSession(t *testing.T) {
	if got := AnchorName("saddle-main"); got != "saddle-main-anchor" {
		t.Fatalf("AnchorName = %q", got)
	}
}

func TestAnchorSpecRunsNothingAndCarriesNothing(t *testing.T) {
	s := AnchorSpec("saddle-main", "saddle-verify:local", "saddle-main-net")

	if s.Name != "saddle-main-anchor" {
		t.Errorf("Name = %q", s.Name)
	}
	if s.Image != "saddle-verify:local" {
		t.Errorf("Image = %q", s.Image)
	}
	if s.Network != "saddle-main-net" {
		t.Errorf("Network = %q", s.Network)
	}
	// The anchor holds an address open. Anything it could read or write is a
	// liability, not a feature: no worktree, no mcp.json, no skills, and
	// above all no CLAUDE_CODE_OAUTH_TOKEN.
	if len(s.Mounts) != 0 {
		t.Errorf("Mounts = %v, want none", s.Mounts)
	}
	if len(s.Env) != 0 {
		t.Errorf("Env = %v, want none", s.Env)
	}
	if s.Workdir != "" {
		t.Errorf("Workdir = %q, want empty", s.Workdir)
	}
}

func TestAnchorSpecIsSmallAndLongLived(t *testing.T) {
	s := AnchorSpec("x", "img", "net")
	if s.CPUs != 1 {
		t.Errorf("CPUs = %d, want 1", s.CPUs)
	}
	if s.Memory != "256m" {
		t.Errorf("Memory = %q, want 256m", s.Memory)
	}
	want := []string{"sleep", "infinity"}
	if len(s.Cmd) != len(want) || s.Cmd[0] != want[0] || s.Cmd[1] != want[1] {
		t.Errorf("Cmd = %v, want %v", s.Cmd, want)
	}
}

// The anchor must use the profile's image, never a hardcoded one: it is the
// image already pulled for the session, so it costs no second pull and there
// is no second thing to keep current.
func TestAnchorSpecUsesWhateverImageItIsGiven(t *testing.T) {
	if got := AnchorSpec("x", "ghcr.io/example/other:9", "net").Image; got != "ghcr.io/example/other:9" {
		t.Fatalf("Image = %q", got)
	}
}
