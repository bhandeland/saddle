package render

import (
	"strings"
	"testing"
)

func TestCmuxArgsBuildsNewWorkspace(t *testing.T) {
	got := CmuxArgs("saddle-demo", "/wt", []string{"container", "start", "-ai", "saddle-demo"})
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"new-workspace", "--name saddle-demo", "--cwd /wt",
		"--command container start -ai saddle-demo",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
}

func TestCmuxArgsQuotesNothingItself(t *testing.T) {
	// The command is passed as one argv element, so no shell quoting is
	// needed and none must be added.
	got := CmuxArgs("n", "/w", []string{"a", "b c"})
	for _, a := range got {
		if strings.Contains(a, `\"`) {
			t.Fatalf("CmuxArgs added escaping: %v", got)
		}
	}
}

func TestAutoFallsBackToTerminalWithoutCmux(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no cmux on PATH
	if got := Auto(); got != ModeTerminal {
		t.Fatalf("Auto() = %q, want terminal", got)
	}
}
