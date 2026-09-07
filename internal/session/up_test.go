package session

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brandon/saddle/internal/profile"
	"github.com/brandon/saddle/internal/render"
)

// nameRE pins the SessionName allowlist: lower-case ASCII letters and
// digits, single hyphens as separators, never leading/trailing/doubled.
var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func TestSessionNameFromRepoAndBranch(t *testing.T) {
	got := SessionName("/Users/brandon/llmworkspace/saddle", "fix-auth")
	if got != "saddle-fix-auth" {
		t.Fatalf("got %q want saddle-fix-auth", got)
	}
}

func TestSessionNameSanitisesSlashes(t *testing.T) {
	got := SessionName("/repo/my.app", "feature/big thing")
	if strings.ContainsAny(got, "/ .") {
		t.Fatalf("unsanitised session name %q", got)
	}
	if !nameRE.MatchString(got) {
		t.Fatalf("SessionName(%q) = %q, does not match allowlist %s", "feature/big thing", got, nameRE)
	}
}

func TestSessionNameStripsShellMetacharacters(t *testing.T) {
	got := SessionName("/repo/app", "x;whoami")
	for _, bad := range []string{";", "|", "&", "$", "`", "(", ")", "<", ">", "'", `"`, "\\", " "} {
		if strings.Contains(got, bad) {
			t.Fatalf("SessionName(%q) = %q, contains %q", "x;whoami", got, bad)
		}
	}
	if !nameRE.MatchString(got) {
		t.Fatalf("SessionName(%q) = %q, does not match allowlist %s", "x;whoami", got, nameRE)
	}
}

func TestSessionNameIsNeverEmpty(t *testing.T) {
	got := SessionName("///", "!!!")
	if got == "" {
		t.Fatal("SessionName must never return an empty string")
	}
	if !nameRE.MatchString(got) {
		t.Fatalf("SessionName(%q) = %q, does not match allowlist %s", "!!!", got, nameRE)
	}
}

func TestClaudeCmdSkipsPermissionsByDefault(t *testing.T) {
	got := strings.Join(ClaudeCmd(false, "/etc/saddle/mcp.json"), " ")
	if !strings.Contains(got, "--dangerously-skip-permissions") {
		t.Fatalf("permissions not disabled by default: %q", got)
	}
	if !strings.Contains(got, "--mcp-config /etc/saddle/mcp.json") {
		t.Fatalf("mcp config not passed: %q", got)
	}
}

func TestClaudeCmdSafeRestoresPrompting(t *testing.T) {
	got := strings.Join(ClaudeCmd(true, "/etc/saddle/mcp.json"), " ")
	if strings.Contains(got, "--dangerously-skip-permissions") {
		t.Fatalf("--safe still disabled permissions: %q", got)
	}
}

func TestMCPConfigEmitsHTTPServers(t *testing.T) {
	p := profile.Profile{CarryIn: profile.CarryIn{MCP: map[string]profile.MCP{
		"remem": {URL: "http://192.168.128.1:9100/mcp", Tools: "read-write"},
	}}}
	data, err := MCPConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		MCPServers map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, data)
	}
	s, ok := got.MCPServers["remem"]
	if !ok {
		t.Fatalf("remem missing: %s", data)
	}
	if s.Type != "http" || s.URL != "http://192.168.128.1:9100/mcp" {
		t.Fatalf("wrong server entry: %+v", s)
	}
}

func TestSkillMountsAreReadOnlyAndTargeted(t *testing.T) {
	got := SkillMounts("/Users/brandon", []string{"remem", "superpowers"})
	if len(got) != 2 {
		t.Fatalf("got %d mounts, want 2", len(got))
	}
	if got[0].Source != "/Users/brandon/.claude/skills/remem" {
		t.Errorf("wrong source: %q", got[0].Source)
	}
	if got[0].Target != "/root/.claude/skills/remem" {
		t.Errorf("wrong target: %q", got[0].Target)
	}
	if !got[0].ReadOnly {
		t.Error("skills must be mounted read-only")
	}
}

func TestSkillMountsEmptyWhenNoneRequested(t *testing.T) {
	if got := SkillMounts("/home/x", nil); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func TestSessionCleanupDirRejectsEmptyWorktree(t *testing.T) {
	if dir, ok := sessionCleanupDir("/home/x/.local/state/saddle/sessions", ""); ok {
		t.Fatalf("expected empty worktree to be rejected, got dir=%q ok=%v", dir, ok)
	}
}

func TestSessionCleanupDirRejectsPathOutsideStateDir(t *testing.T) {
	if dir, ok := sessionCleanupDir("/home/x/.local/state/saddle/sessions", "/home/x/somewhere-else/worktree"); ok {
		t.Fatalf("expected out-of-place worktree to be rejected, got dir=%q ok=%v", dir, ok)
	}
}

func TestSessionCleanupDirAcceptsPathUnderStateDir(t *testing.T) {
	stateDir := "/home/x/.local/state/saddle/sessions"
	dir, ok := sessionCleanupDir(stateDir, stateDir+"/my-session.d/worktree")
	if !ok {
		t.Fatal("expected in-place worktree to be accepted")
	}
	if dir != stateDir+"/my-session.d" {
		t.Fatalf("got %q want %q", dir, stateDir+"/my-session.d")
	}
}

func TestMCPConfigEmptyWhenNoServers(t *testing.T) {
	data, err := MCPConfig(profile.Profile{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "mcpServers") {
		t.Fatalf("expected an mcpServers key even when empty: %s", data)
	}
}

// A colliding --name must not be able to adopt a live session's directory:
// os.MkdirAll would have succeeded on an existing directory, after which
// the caller's cleanup unwind would delete the running session's worktree
// and uncommitted work.
func TestClaimSessionDirRefusesExistingDirectory(t *testing.T) {
	stateDir := t.TempDir()

	dir, err := claimSessionDir(stateDir, "repo-fix")
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	live := filepath.Join(dir, "worktree", "uncommitted.txt")
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("work in progress"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := claimSessionDir(stateDir, "repo-fix"); err == nil {
		t.Fatal("second claim on an existing session directory must fail")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error should name the collision, got %v", err)
	}

	// The failed claim must leave the live session entirely alone.
	data, err := os.ReadFile(live)
	if err != nil {
		t.Fatalf("failed claim removed the live session's worktree: %v", err)
	}
	if string(data) != "work in progress" {
		t.Fatalf("live session content changed: %q", data)
	}
}

func TestClaimSessionDirCreatesDirectoryPrivately(t *testing.T) {
	stateDir := t.TempDir()
	dir, err := claimSessionDir(stateDir, "repo-fix")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(stateDir, "repo-fix.d"); dir != want {
		t.Fatalf("got %q want %q", dir, want)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Fatalf("session directory mode %o, want 700", perm)
	}
}

// A session directory always sits strictly below the state directory. A
// candidate equal to it means the worktree path was malformed, and removing
// it would take every other session's directory with it.
func TestSessionCleanupDirRejectsStateDirItself(t *testing.T) {
	// Each of these has Dir() equal to the state directory itself.
	for _, worktree := range []string{
		"/state/saddle/sessions/worktree",
		"/state/saddle/sessions/x",
		"/state/saddle/sessions/",
	} {
		if got, ok := sessionCleanupDir("/state/saddle/sessions", worktree); ok {
			t.Fatalf("sessionCleanupDir(%q) = %q, must refuse the state directory itself", worktree, got)
		}
	}
}

func TestEgressSummaryDescribesPosture(t *testing.T) {
	for _, tc := range []struct {
		name            string
		open, noNet     bool
		allow           []string
		want, wantLabel string
	}{
		{name: "open", open: true, want: "open", wantLabel: "OPEN(!)"},
		{name: "no-net", noNet: true, want: "none", wantLabel: "none"},
		{name: "empty allowlist", want: "none", wantLabel: "none"},
		{
			name: "one host", allow: []string{"api.anthropic.com"},
			want: "api.anthropic.com", wantLabel: "api.anthropic.com",
		},
		{
			name: "several hosts", allow: []string{"api.anthropic.com", "proxy.golang.org", "sum.golang.org"},
			want: "api.anthropic.com +2", wantLabel: "api.anthropic.com +2",
		},
	} {
		got := egressSummary(tc.open, tc.noNet, tc.allow)
		if got != tc.want {
			t.Fatalf("%s: egressSummary = %q, want %q", tc.name, got, tc.want)
		}
		if label := (State{Egress: got}).EgressLabel(); label != tc.wantLabel {
			t.Fatalf("%s: EgressLabel = %q, want %q", tc.name, label, tc.wantLabel)
		}
	}
}

// State files written before Egress existed must not read as reassuringly
// blank.
func TestEgressLabelUnknownForOlderStateFiles(t *testing.T) {
	if got := (State{}).EgressLabel(); got != "unknown" {
		t.Fatalf("EgressLabel for a pre-Egress state file = %q, want unknown", got)
	}
}

func TestPrintSummaryShowsNameProfileWorktreeAndEgress(t *testing.T) {
	st := State{
		Name: "saddle-fix", Worktree: "/state/saddle-fix.d/worktree",
		Branch: "saddle/fix", Egress: "api.anthropic.com +1",
	}
	var b strings.Builder
	printSummary(&b, st, profile.Profile{Name: "go"}, false,
		[]string{"api.anthropic.com", "proxy.golang.org"}, render.ModeCmux)
	out := b.String()
	for _, want := range []string{
		"saddle-fix", "go (detected)", "/state/saddle-fix.d/worktree",
		"saddle/fix", "api.anthropic.com, proxy.golang.org", "all else denied", "cmux",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestPrintSummaryFlagsOpenEgressAndNamedProfile(t *testing.T) {
	var b strings.Builder
	printSummary(&b, State{Name: "s", Egress: "open"}, profile.Profile{Name: "go"},
		true, nil, render.ModeTerminal)
	out := b.String()
	if !strings.Contains(out, "OPEN") {
		t.Fatalf("an unfiltered session must be flagged:\n%s", out)
	}
	if !strings.Contains(out, "go (named)") {
		t.Fatalf("an explicitly named profile must say so:\n%s", out)
	}
}

func TestStartSpawnedRunsNothingWhenNoServerDeclaresSpawn(t *testing.T) {
	p := profile.Profile{CarryIn: profile.CarryIn{MCP: map[string]profile.MCP{
		"other": {URL: "http://127.0.0.1:9100/mcp"},
	}}}
	procs, rec, err := startSpawned(p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 0 || len(rec) != 0 {
		t.Fatalf("started something: %v %v", procs, rec)
	}
}

func TestStartSpawnedWaitsForTheServerToBind(t *testing.T) {
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	p := profile.Profile{CarryIn: profile.CarryIn{MCP: map[string]profile.MCP{
		"fake": {
			URL: "http://" + addr + "/mcp",
			// A listener that binds and holds, with no dependence on which
			// netcat macOS ships. Started as a child exactly as a real
			// server would be.
			Spawn: listenerCmd(port),
		},
	}}}

	procs, rec, err := startSpawned(p, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, pr := range procs {
			_ = pr.Kill()
		}
	}()

	if len(procs) != 1 {
		t.Fatalf("want 1 process, got %d", len(procs))
	}
	if len(rec) != 1 || rec[0].Name != "fake" || rec[0].PID != procs[0].PID {
		t.Fatalf("bad record %+v", rec)
	}
}

func TestStartSpawnedFailsWhenTheServerNeverBinds(t *testing.T) {
	port := freePort(t)
	p := profile.Profile{CarryIn: profile.CarryIn{MCP: map[string]profile.MCP{
		"fake": {
			URL:   fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
			Spawn: []string{"sleep", "30"},
		},
	}}}

	procs, _, err := startSpawned(p, 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected a readiness failure")
	}
	if len(procs) != 0 {
		t.Fatal("a failed start must leave nothing running")
	}
}

func TestStartSpawnedFailsWhenTheBinaryIsMissing(t *testing.T) {
	p := profile.Profile{CarryIn: profile.CarryIn{MCP: map[string]profile.MCP{
		"fake": {
			URL:   "http://127.0.0.1:9100/mcp",
			Spawn: []string{"saddle-no-such-binary-xyz"},
		},
	}}}
	if _, _, err := startSpawned(p, time.Second); err == nil {
		t.Fatal("expected an error for a missing binary")
	}
}

func TestStartSpawnedRejectsASpawnWithNoUsableURL(t *testing.T) {
	// spawn without a port to poll cannot be waited on, and a server that is
	// started but never checked is exactly the silent half-working state the
	// design refuses.
	p := profile.Profile{CarryIn: profile.CarryIn{MCP: map[string]profile.MCP{
		"fake": {URL: "http://127.0.0.1/mcp", Spawn: []string{"sleep", "30"}},
	}}}
	if _, _, err := startSpawned(p, time.Second); err == nil {
		t.Fatal("expected an error for a URL with no port")
	}
}

func TestStateRoundTripsSpawnedProcesses(t *testing.T) {
	st := State{
		Name: "saddle-x", Repo: "/r", Worktree: "/w", Branch: "b",
		Spawned: []Spawned{{Name: "remem", PID: 4242, Cmd: []string{"remem", "serve"}}},
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var back State
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Spawned) != 1 || back.Spawned[0].PID != 4242 ||
		back.Spawned[0].Cmd[0] != "remem" {
		t.Fatalf("bad round trip: %+v", back.Spawned)
	}
}

// listenerCmd binds port and sleeps, standing in for a spawned MCP server.
func listenerCmd(port int) []string {
	return []string{
		"python3", "-c",
		"import socket,time;s=socket.socket();" +
			"s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);" +
			"s.bind(('127.0.0.1'," + strconv.Itoa(port) + "));s.listen();time.sleep(30)",
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}
