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
      tools: read-write
  ssh_agent: false
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
	if p.CarryIn.MCP["remem"].Tools != "read-write" {
		t.Fatalf("mcp wrong: %+v", p.CarryIn.MCP)
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
