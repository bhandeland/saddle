package profile

import "testing"

func TestParseReadsAllFields(t *testing.T) {
	p, err := Parse([]byte(`
name: go
image: ghcr.io/brandon/saddle-go:1.24
detect: [go.mod]
resources: {cpus: 4, memory: 8g}
egress:
  allow: [api.anthropic.com, proxy.golang.org]
carry_in:
  skills: [remem]
  mcp:
    remem:
      url: http://{{gateway}}:9100/mcp
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Name != "go" || p.Resources.CPUs != 4 || p.Resources.Memory != "8g" {
		t.Fatalf("scalar fields wrong: %+v", p)
	}
	if len(p.Egress.Allow) != 2 || p.Egress.Allow[0] != "api.anthropic.com" {
		t.Fatalf("egress wrong: %+v", p.Egress)
	}
	if p.CarryIn.MCP["remem"].URL != "http://{{gateway}}:9100/mcp" {
		t.Fatalf("mcp wrong: %+v", p.CarryIn.MCP)
	}
	if len(p.CarryIn.Skills) != 1 || p.CarryIn.Skills[0] != "remem" {
		t.Fatalf("skills wrong: %+v", p.CarryIn.Skills)
	}
}

// Both fields below parse but are never acted on. Accepting them silently
// would misrepresent the containment, so Parse must refuse them.
func TestParseRejectsUnimplementedSSHAgent(t *testing.T) {
	if _, err := Parse([]byte("name: go\ncarry_in:\n  ssh_agent: true\n")); err == nil {
		t.Fatal("expected error for carry_in.ssh_agent: true")
	}
}

func TestParseAllowsSSHAgentSetToFalse(t *testing.T) {
	if _, err := Parse([]byte("name: go\ncarry_in:\n  ssh_agent: false\n")); err != nil {
		t.Fatalf("ssh_agent: false is the zero value and must be accepted: %v", err)
	}
}

func TestParseRejectsUnimplementedMCPTools(t *testing.T) {
	_, err := Parse([]byte(`
name: go
carry_in:
  mcp:
    remem:
      url: http://x/mcp
      tools: recall
`))
	if err == nil {
		t.Fatal("expected error for carry_in.mcp.<name>.tools")
	}
}

func TestParseRejectsMissingName(t *testing.T) {
	if _, err := Parse([]byte("image: alpine\n")); err == nil {
		t.Fatal("expected error for profile with no name")
	}
}

func TestDetectFirstMatchWins(t *testing.T) {
	profiles := []Profile{
		{Name: "go", Detect: []string{"go.mod"}},
		{Name: "node", Detect: []string{"package.json"}},
	}
	got, ok := Detect(profiles, []string{"README.md", "package.json"})
	if !ok || got.Name != "node" {
		t.Fatalf("got %+v ok=%v, want node", got, ok)
	}
}

func TestDetectNoMatch(t *testing.T) {
	profiles := []Profile{{Name: "go", Detect: []string{"go.mod"}}}
	if _, ok := Detect(profiles, []string{"README.md"}); ok {
		t.Fatal("expected no match")
	}
}

func TestExpandSubstitutesInMCPURL(t *testing.T) {
	p := Profile{CarryIn: CarryIn{MCP: map[string]MCP{
		"remem": {URL: "http://{{gateway}}:9100/mcp", Tools: "read-write"},
	}}}
	// proxy_port is supplied but unused here: extra vars must be harmless.
	out := Expand(p, map[string]string{"gateway": "192.168.128.1", "proxy_port": "9000"})
	want := "http://192.168.128.1:9100/mcp"
	if out.CarryIn.MCP["remem"].URL != want {
		t.Fatalf("got %q want %q", out.CarryIn.MCP["remem"].URL, want)
	}
}

func TestExpandDoesNotMutateInput(t *testing.T) {
	orig := "http://{{gateway}}/mcp"
	p := Profile{CarryIn: CarryIn{MCP: map[string]MCP{"remem": {URL: orig}}}}
	_ = Expand(p, map[string]string{"gateway": "10.0.0.1"})
	if p.CarryIn.MCP["remem"].URL != orig {
		t.Fatal("Expand mutated its input")
	}
}

func TestSpawnParses(t *testing.T) {
	p, err := Parse([]byte(`
name: go
carry_in:
  mcp:
    remem:
      spawn: ["remem", "serve", "--http", "--host", "{{gateway}}"]
      url: http://{{gateway}}:9100/mcp
`))
	if err != nil {
		t.Fatal(err)
	}
	got := p.CarryIn.MCP["remem"].Spawn
	want := []string{"remem", "serve", "--http", "--host", "{{gateway}}"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestExpandRewritesSpawnArgs(t *testing.T) {
	p := Profile{CarryIn: CarryIn{MCP: map[string]MCP{"remem": {
		URL:   "http://{{gateway}}:9100/mcp",
		Spawn: []string{"remem", "serve", "--host", "{{gateway}}", "--project", "{{repo}}"},
	}}}}
	out := Expand(p, map[string]string{"gateway": "192.168.64.3", "repo": "saddle"})

	got := out.CarryIn.MCP["remem"].Spawn
	want := []string{"remem", "serve", "--host", "192.168.64.3", "--project", "saddle"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: got %q want %q", i, got[i], want[i])
		}
	}
	if out.CarryIn.MCP["remem"].URL != "http://192.168.64.3:9100/mcp" {
		t.Fatalf("url not expanded: %q", out.CarryIn.MCP["remem"].URL)
	}
}

func TestExpandDoesNotMutateTheInputSpawn(t *testing.T) {
	// Expand returns a copy. A shared backing array would let one session's
	// gateway leak into another profile value.
	orig := []string{"remem", "--host", "{{gateway}}"}
	p := Profile{CarryIn: CarryIn{MCP: map[string]MCP{"remem": {Spawn: orig}}}}
	Expand(p, map[string]string{"gateway": "192.168.64.3"})
	if orig[2] != "{{gateway}}" {
		t.Fatalf("input mutated: %q", orig[2])
	}
}

func TestProfileWithURLAndNoSpawnIsValid(t *testing.T) {
	p, err := Parse([]byte(`
name: go
carry_in:
  mcp:
    other:
      url: http://{{gateway}}:7000/mcp
`))
	if err != nil {
		t.Fatal(err)
	}
	if p.CarryIn.MCP["other"].Spawn != nil {
		t.Fatal("expected no spawn")
	}
}
