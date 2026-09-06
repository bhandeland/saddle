package hostsvc

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAddrFromURL(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"http://192.168.64.3:9100/mcp", "192.168.64.3:9100"},
		{"http://127.0.0.1:9100/mcp", "127.0.0.1:9100"},
	} {
		got, err := AddrFromURL(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%s: got %q want %q", c.in, got, c.want)
		}
	}
}

func TestAddrFromURLRejectsAURLWithNoPort(t *testing.T) {
	// A spawned server's readiness check needs a concrete port. Defaulting to
	// 80 would poll the wrong socket and report ready when nothing is there.
	if _, err := AddrFromURL("http://192.168.64.3/mcp"); err == nil {
		t.Fatal("expected an error for a URL with no port")
	}
}

func TestStartRunsTheCommandAndKillStopsIt(t *testing.T) {
	p, err := Start([]string{"sleep", "30"})
	if err != nil {
		t.Fatal(err)
	}
	if p.PID <= 0 {
		t.Fatalf("bad pid %d", p.PID)
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	// A killed and reaped process is no longer signalable.
	if err := exec.Command("kill", "-0", itoa(p.PID)).Run(); err == nil {
		t.Fatal("process still alive after Kill")
	}
}

func TestStartRejectsAnEmptyCommand(t *testing.T) {
	if _, err := Start(nil); err == nil {
		t.Fatal("expected an error for an empty command")
	}
}

func TestStartFailsWhenTheBinaryDoesNotExist(t *testing.T) {
	if _, err := Start([]string{"saddle-no-such-binary-xyz"}); err == nil {
		t.Fatal("expected an error for a missing binary")
	}
}

func TestWaitReadyReturnsOnceSomethingIsListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := WaitReady(ln.Addr().String(), 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestWaitReadyTimesOutWhenNothingBinds(t *testing.T) {
	// Bind and immediately close, so the port is almost certainly free.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	start := time.Now()
	if err := WaitReady(addr, 300*time.Millisecond); err == nil {
		t.Fatal("expected a timeout")
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Fatal("returned before the timeout elapsed")
	}
}

func TestReapKillsAMatchingProcess(t *testing.T) {
	p, err := Start([]string{"sleep", "30"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	if err := Reap(p.PID, p.Cmd, p.Line); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("kill", "-0", itoa(p.PID)).Run(); err == nil {
		t.Fatal("process still alive after Reap")
	}
}

func TestReapRefusesWhenTheCommandDoesNotMatch(t *testing.T) {
	// The pid-reuse guard. Down runs from a state file that may be old; a
	// recycled pid must never be killed.
	p, err := Start([]string{"sleep", "30"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()

	// A mismatch is not an error: it says our child is already gone, which is
	// the ordinary teardown outcome. Returning an error here used to abort
	// `saddle down` after the container and network were gone but before the
	// worktree and state file were, leaving a session only --force could
	// remove - and --force also skips the uncommitted-changes guard.
	if err := Reap(p.PID, []string{"some-other-command"}, "some-other-command"); err != nil {
		t.Fatalf("expected a mismatch to be reported as nothing to do, got %v", err)
	}
	if err := exec.Command("kill", "-0", itoa(p.PID)).Run(); err != nil {
		t.Fatal("Reap killed a process whose command did not match")
	}
}

func TestStartAndReapHandleAShebangScript(t *testing.T) {
	// The regression this package exists to avoid. Exec'ing a shebang script
	// makes the kernel rewrite argv: it drops the caller's argv[0] and
	// prepends the interpreter, so the live command line is not the argv we
	// asked for. remem - the one command this feature was written to run - is
	// such a script, so matching on the joined argv alone matched nothing.
	dir := t.TempDir()
	script := filepath.Join(dir, "sleeper.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 20\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := Start([]string{script})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()

	if p.Line == strings.Join(p.Cmd, " ") {
		t.Fatalf("expected the observed line to differ from the argv, both were %q", p.Line)
	}
	if !strings.Contains(p.Line, script) {
		t.Fatalf("observed line %q does not mention %q", p.Line, script)
	}

	if err := Reap(p.PID, p.Cmd, p.Line); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("kill", "-0", itoa(p.PID)).Run(); err == nil {
		t.Fatal("Reap did not kill a shebang script it started")
	}
}

func TestStartRecordsTheObservedCommandLine(t *testing.T) {
	// For a plain binary the observed line and the joined argv agree, so
	// state written before Line existed still matches.
	p, err := Start([]string{"sleep", "30"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	if p.Line != strings.Join(p.Cmd, " ") {
		t.Fatalf("got %q, want %q", p.Line, strings.Join(p.Cmd, " "))
	}
}

func TestReapOnAPidThatIsGoneIsNotAnError(t *testing.T) {
	// Teardown of an already-dead child is the normal case, not a failure.
	if err := Reap(999999, []string{"sleep", "30"}, "sleep 30"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
