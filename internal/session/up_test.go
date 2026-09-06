package session

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/brandon/saddle/internal/profile"
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

func TestMCPConfigEmptyWhenNoServers(t *testing.T) {
	data, err := MCPConfig(profile.Profile{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "mcpServers") {
		t.Fatalf("expected an mcpServers key even when empty: %s", data)
	}
}
