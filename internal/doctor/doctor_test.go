package doctor

import (
	"errors"
	"strings"
	"testing"
)

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

func TestDecideCanaryAndListenerResultsServiceFailed(t *testing.T) {
	// Service check failed, lsof works, no exposed listeners.
	// Should skip canary and report lsof OK.
	results := decideCanaryAndListenerResults(false, nil, "")
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Name != "can run containers" || results[0].OK {
		t.Errorf("first result should be canary skip: %+v", results[0])
	}
	if !strings.Contains(results[0].Detail, "skipped") {
		t.Errorf("canary detail should mention skipped: %s", results[0].Detail)
	}
	if results[1].Name != "host services exposed" || !results[1].OK {
		t.Errorf("second result should be lsof OK: %+v", results[1])
	}
}

func TestDecideCanaryAndListenerResultsLsofError(t *testing.T) {
	// Service passed, lsof error (e.g., not found or sandboxed).
	// Should report lsof error, not silently omit the check.
	lsofErr := errors.New("lsof: not found")
	results := decideCanaryAndListenerResults(true, lsofErr, "")
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (just lsof error)", len(results))
	}
	if results[0].Name != "host services exposed" || results[0].OK {
		t.Errorf("result should be lsof error: %+v", results[0])
	}
	if !strings.Contains(results[0].Detail, "could not enumerate") {
		t.Errorf("detail should mention enumerate error: %s", results[0].Detail)
	}
}

func TestDecideCanaryAndListenerResultsExposedListeners(t *testing.T) {
	// Service passed, lsof works, found exposed listeners.
	results := decideCanaryAndListenerResults(true, nil,
		`COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
node 1 b 3u IPv4 0x1 0t0 TCP *:3000 (LISTEN)`)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (just lsof warning)", len(results))
	}
	if results[0].Name != "host services exposed" || results[0].OK {
		t.Errorf("result should be lsof warning: %+v", results[0])
	}
	if !strings.Contains(results[0].Detail, "node") || !strings.Contains(results[0].Detail, "3000") {
		t.Errorf("detail should contain exposed listener: %s", results[0].Detail)
	}
}
