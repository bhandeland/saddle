// Package profile turns declarative profile config into a concrete plan.
// It is a pure package: config in, values out, no I/O beyond what the
// caller hands it.
package profile

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type Resources struct {
	CPUs   int    `yaml:"cpus"`
	Memory string `yaml:"memory"`
}

type Egress struct {
	Allow []string `yaml:"allow"`
}

type MCP struct {
	URL   string `yaml:"url"`
	Tools string `yaml:"tools"`
}

type CarryIn struct {
	Skills   []string       `yaml:"skills"`
	MCP      map[string]MCP `yaml:"mcp"`
	SSHAgent bool           `yaml:"ssh_agent"`
}

type Profile struct {
	Name      string    `yaml:"name"`
	Image     string    `yaml:"image"`
	Detect    []string  `yaml:"detect"`
	Resources Resources `yaml:"resources"`
	Egress    Egress    `yaml:"egress"`
	CarryIn   CarryIn   `yaml:"carry_in"`
}

// Parse decodes one profile document.
func Parse(data []byte) (Profile, error) {
	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return Profile{}, fmt.Errorf("profile: %w", err)
	}
	if strings.TrimSpace(p.Name) == "" {
		return Profile{}, errors.New("profile: name is required")
	}
	// Both fields below are parsed but nothing reads them. A security-
	// relevant field that silently does nothing is worse than one that does
	// not exist: `ssh_agent: true` would look like the agent socket had been
	// forwarded, and `tools: recall` would look like the MCP server had been
	// restricted to reads, when neither is true. Refuse instead.
	if p.CarryIn.SSHAgent {
		return Profile{}, errors.New("profile: carry_in.ssh_agent is not yet implemented; remove it rather than relying on it")
	}
	for name, m := range p.CarryIn.MCP {
		if strings.TrimSpace(m.Tools) != "" {
			return Profile{}, fmt.Errorf("profile: carry_in.mcp.%s.tools is not yet implemented; every carried-in server gets the tools it exposes", name)
		}
	}
	return p, nil
}

// Detect returns the first profile any of whose Detect entries appears in
// present. Profiles are evaluated in the order given, so callers that need
// determinism must sort before calling.
func Detect(profiles []Profile, present []string) (Profile, bool) {
	set := make(map[string]bool, len(present))
	for _, f := range present {
		set[f] = true
	}
	for _, p := range profiles {
		for _, d := range p.Detect {
			if set[d] {
				return p, true
			}
		}
	}
	return Profile{}, false
}

// Expand substitutes {{key}} placeholders with session-scoped values that are
// not known until the container exists. It returns a copy; the input is not
// modified.
func Expand(p Profile, vars map[string]string) Profile {
	rep := make([]string, 0, len(vars)*2)
	for k, v := range vars {
		rep = append(rep, "{{"+k+"}}", v)
	}
	r := strings.NewReplacer(rep...)

	out := p
	if p.CarryIn.MCP != nil {
		m := make(map[string]MCP, len(p.CarryIn.MCP))
		for name, s := range p.CarryIn.MCP {
			s.URL = r.Replace(s.URL)
			m[name] = s
		}
		out.CarryIn.MCP = m
	}
	return out
}
