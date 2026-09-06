package hostsvc

import (
	"net"
	"os/exec"
	"strconv"
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
	if err := Reap(p.PID, p.Cmd); err != nil {
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

	if err := Reap(p.PID, []string{"some-other-command"}); err == nil {
		t.Fatal("expected Reap to refuse a mismatched command")
	}
	if err := exec.Command("kill", "-0", itoa(p.PID)).Run(); err != nil {
		t.Fatal("Reap killed a process whose command did not match")
	}
}

func TestReapOnAPidThatIsGoneIsNotAnError(t *testing.T) {
	// Teardown of an already-dead child is the normal case, not a failure.
	if err := Reap(999999, []string{"sleep", "30"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
