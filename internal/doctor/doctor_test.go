package doctor

import "testing"

func TestParseListenersFindsWildcardBinds(t *testing.T) {
	// Trimmed real `lsof -nP -iTCP -sTCP:LISTEN` output.
	out := `COMMAND   PID     USER   FD   TYPE DEVICE SIZE/OFF NODE NAME
node    12345  brandon   23u  IPv4 0x1234      0t0  TCP *:3000 (LISTEN)
postgres 234   brandon    7u  IPv4 0x5678      0t0  TCP 127.0.0.1:5432 (LISTEN)
python  4567   brandon    3u  IPv4 0x9abc      0t0  TCP 0.0.0.0:18099 (LISTEN)`
	got := ParseListeners(out)
	if len(got) != 2 {
		t.Fatalf("got %d listeners %v, want 2", len(got), got)
	}
	if got[0] != "node *:3000" || got[1] != "python 0.0.0.0:18099" {
		t.Fatalf("unexpected listeners: %v", got)
	}
}

func TestParseListenersIgnoresLoopbackOnly(t *testing.T) {
	out := `COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
pg 1 b 7u IPv4 0x1 0t0 TCP 127.0.0.1:5432 (LISTEN)`
	if got := ParseListeners(out); len(got) != 0 {
		t.Fatalf("loopback-only bind reported as exposed: %v", got)
	}
}

func TestParseListenersHandlesEmptyInput(t *testing.T) {
	if got := ParseListeners(""); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}
