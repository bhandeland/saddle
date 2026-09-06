package render

import (
	"context"
	"strings"
	"testing"
)

func TestCmuxArgsBuildsNewWorkspace(t *testing.T) {
	got := CmuxArgs("saddle-demo", "/wt", []string{"container", "start", "-ai", "saddle-demo"})
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"new-workspace", "--name saddle-demo", "--cwd /wt",
		"--command 'container' 'start' '-ai' 'saddle-demo'",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
}

func TestCmuxArgsQuotesEachElement(t *testing.T) {
	got := CmuxArgs("n", "/w", []string{"container", "start", "-ai", "my-session"})
	cmd := got[len(got)-1] // The --command value is the last element
	// Each element should be single-quoted
	for _, want := range []string{"'container'", "'start'", "'-ai'", "'my-session'"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing single-quoted element %q in command: %q", want, cmd)
		}
	}
}

func TestCmuxArgsNeutralisesShellMetacharacters(t *testing.T) {
	got := CmuxArgs("n", "/w", []string{"container", "start", "-ai", "x;whoami"})
	cmd := got[len(got)-1]
	// The dangerous unquoted sequence should not appear
	if strings.Contains(cmd, "-ai x;whoami") {
		t.Fatalf("shell metacharacter not neutralized in command: %q", cmd)
	}
	// The session element should be single-quoted instead
	if !strings.Contains(cmd, "'x;whoami'") {
		t.Errorf("dangerous element not properly quoted in: %q", cmd)
	}
}

func TestCmuxArgsHandlesEmbeddedSingleQuote(t *testing.T) {
	got := CmuxArgs("n", "/w", []string{"container", "run", "it's-me"})
	cmd := got[len(got)-1]
	// The embedded quote should be escaped as '\''
	if !strings.Contains(cmd, `'\''`) {
		t.Errorf("embedded single quote not properly escaped in: %q", cmd)
	}
}

func TestAutoFallsBackToTerminalWithoutCmux(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no cmux on PATH
	if got := Auto(); got != ModeTerminal {
		t.Fatalf("Auto() = %q, want terminal", got)
	}
}

func TestAttachRejectsEmptyArgv(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []Mode{ModeTerminal, ModeCmux} {
		err := Attach(ctx, mode, "test", "/tmp", []string{})
		if err == nil {
			t.Fatalf("Attach(%q) with empty argv should error, got nil", mode)
		}
	}
}
