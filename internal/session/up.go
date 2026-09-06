package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brandon/saddle/internal/auth"
	"github.com/brandon/saddle/internal/config"
	"github.com/brandon/saddle/internal/egress"
	"github.com/brandon/saddle/internal/profile"
	"github.com/brandon/saddle/internal/render"
	"github.com/brandon/saddle/internal/runtime"
	"github.com/brandon/saddle/internal/worktree"
)

type UpOptions struct {
	Repo        string
	Name        string
	ProfileName string
	Safe        bool
	NoNet       bool
	OpenNet     bool
	ExtraAllow  []string
	Render      render.Mode
}

// SessionName derives a pronounceable session name from the repo and branch.
//
// The result becomes both a container name and part of a string that cmux
// types into an interactive shell, so it is restricted to a strict
// allowlist (lower-case ASCII letters, digits, and '-') rather than merely
// blocking known-bad characters.
func SessionName(repo, branch string) string {
	return clean(filepath.Base(repo)) + "-" + clean(branch)
}

// clean reduces s to lower-case ASCII letters, digits, and '-'. Every other
// byte becomes '-'; runs of '-' collapse to one; leading and trailing '-'
// are trimmed. An all-disallowed input becomes "x" so the result is never
// empty.
func clean(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
		} else {
			b.WriteByte('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
	if out == "" {
		out = "x"
	}
	return out
}

// ClaudeCmd builds the in-container command. Permissions are disabled by
// default: that is the entire point of the containment.
func ClaudeCmd(safe bool, mcpPath string) []string {
	cmd := []string{"claude"}
	if !safe {
		cmd = append(cmd, "--dangerously-skip-permissions")
	}
	if mcpPath != "" {
		cmd = append(cmd, "--mcp-config", mcpPath)
	}
	return cmd
}

// MCPConfig renders the profile's carried-in MCP servers as Claude's config
// format. Servers are HTTP endpoints reachable at the host gateway.
func MCPConfig(p profile.Profile) ([]byte, error) {
	type server struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	doc := struct {
		MCPServers map[string]server `json:"mcpServers"`
	}{MCPServers: map[string]server{}}
	for name, m := range p.CarryIn.MCP {
		doc.MCPServers[name] = server{Type: "http", URL: m.URL}
	}
	return json.MarshalIndent(doc, "", "  ")
}

// SkillMounts maps carried-in skills from the host into the container,
// read-only. Skills are files, so mounting is all that is required.
func SkillMounts(home string, skills []string) []runtime.Mount {
	out := make([]runtime.Mount, 0, len(skills))
	for _, s := range skills {
		out = append(out, runtime.Mount{
			Source:   filepath.Join(home, ".claude", "skills", s),
			Target:   "/root/.claude/skills/" + s,
			ReadOnly: true,
		})
	}
	return out
}

// Up creates a contained session and attaches to it.
func Up(ctx context.Context, o UpOptions) (State, error) {
	if o.NoNet && o.OpenNet {
		return State{}, fmt.Errorf("--no-net and --open-net are mutually exclusive")
	}

	// Resources are acquired one at a time below. ok is disarmed (set true)
	// only once the session is fully recorded and reachable; until then this
	// defer unwinds anything that was actually created, in reverse order, so
	// no early return leaves an orphaned worktree, network, proxy, or
	// container that saddle down can never find (nothing was Saved yet).
	var (
		sessDirCreated bool
		sessDir        string
		wtCreated      bool
		wtPath         string
		netCreated     bool
		netName        string
		px             *egress.Proxy
		containerID    string
	)
	ok := false
	defer func() {
		if ok {
			return
		}
		if containerID != "" {
			_ = runtime.Remove(ctx, runtime.Handle{ID: containerID})
		}
		if px != nil {
			px.Close()
		}
		if netCreated {
			_ = runtime.DeleteNetwork(ctx, netName)
		}
		if wtCreated {
			_ = worktree.Remove(ctx, o.Repo, wtPath)
		}
		if sessDirCreated {
			_ = os.RemoveAll(sessDir)
		}
	}()

	// 1. Resolve the profile.
	dir, err := config.ProfilesDir()
	if err != nil {
		return State{}, err
	}
	profiles, err := config.LoadProfiles(dir)
	if err != nil {
		return State{}, err
	}
	var p profile.Profile
	if o.ProfileName != "" {
		for _, c := range profiles {
			if c.Name == o.ProfileName {
				p = c
			}
		}
		if p.Name == "" {
			return State{}, fmt.Errorf("no profile named %q in %s", o.ProfileName, dir)
		}
	} else {
		present, err := config.PresentFiles(o.Repo)
		if err != nil {
			return State{}, err
		}
		var ok bool
		if p, ok = profile.Detect(profiles, present); !ok {
			return State{}, fmt.Errorf("no profile matched %s; pass --profile", o.Repo)
		}
	}

	// 2. Names and paths.
	branch := o.Name
	if branch == "" {
		branch = fmt.Sprintf("s%d", time.Now().Unix())
	}
	name := SessionName(o.Repo, branch)
	stateDir, err := Dir()
	if err != nil {
		return State{}, err
	}
	sessDir = filepath.Join(stateDir, name+".d")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		return State{}, err
	}
	sessDirCreated = true
	wtPath = filepath.Join(sessDir, "worktree")

	// 3. Worktree.
	if err := worktree.Create(ctx, o.Repo, "saddle/"+branch, wtPath); err != nil {
		return State{}, err
	}
	wtCreated = true

	// 4. Network first, so the gateway is known before env is fixed.
	netName = name + "-net"
	n, err := runtime.CreateNetwork(ctx, netName, !o.OpenNet)
	if err != nil {
		return State{}, err
	}
	netCreated = true

	// 5. Egress proxy bound to the gateway. --open-net means no filtering at
	// all, so no proxy is started: a listening proxy nobody uses is just a
	// stray port and a leaked goroutine.
	var proxyAddr, proxyPort string
	if !o.OpenNet {
		allow := append([]string{}, p.Egress.Allow...)
		allow = append(allow, o.ExtraAllow...)
		if o.NoNet {
			allow = nil
		}
		px = egress.New(allow)
		proxyAddr, err = px.Listen(n.Gateway + ":0")
		if err != nil {
			return State{}, fmt.Errorf("bind egress proxy on %s: %w", n.Gateway, err)
		}
		_, proxyPort, _ = strings.Cut(proxyAddr, ":")
	}

	// 6. Expand profile placeholders now that the gateway is known.
	p = profile.Expand(p, map[string]string{
		"gateway":    n.Gateway,
		"proxy_port": proxyPort,
	})

	// 7. MCP config, mounted read-only so it never dirties the worktree.
	mcpHost := filepath.Join(sessDir, "mcp.json")
	mcpDoc, err := MCPConfig(p)
	if err != nil {
		return State{}, err
	}
	if err := os.WriteFile(mcpHost, mcpDoc, 0o600); err != nil {
		return State{}, err
	}

	// 8. Token and home, for carried-in skills.
	token, err := auth.Load()
	if err != nil {
		return State{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return State{}, err
	}

	// 9. Container.
	env := map[string]string{
		"CLAUDE_CODE_OAUTH_TOKEN": token,
	}
	if o.OpenNet {
		fmt.Fprintln(os.Stderr, "WARNING: --open-net disables all egress filtering")
	} else {
		env["HTTPS_PROXY"] = "http://" + proxyAddr
		env["HTTP_PROXY"] = "http://" + proxyAddr
	}
	h, err := runtime.Create(ctx, runtime.Spec{
		Name:    name,
		Image:   p.Image,
		Network: netName,
		Workdir: "/work",
		Cmd:     ClaudeCmd(o.Safe, "/etc/saddle/mcp.json"),
		Env:     env,
		Mounts: append([]runtime.Mount{
			{Source: wtPath, Target: "/work"},
			{Source: mcpHost, Target: "/etc/saddle/mcp.json", ReadOnly: true},
		}, SkillMounts(home, p.CarryIn.Skills)...),
		CPUs:   p.Resources.CPUs,
		Memory: p.Resources.Memory,
	})
	if err != nil {
		return State{}, err
	}
	containerID = h.ID

	// 10. Persist before attaching, so a crash during attach is recoverable.
	st := State{
		Name: name, Profile: p.Name, Repo: o.Repo, Worktree: wtPath,
		Branch: "saddle/" + branch, Network: netName, Container: h.ID,
		ProxyAddr: proxyAddr, Status: "running", Created: time.Now().UTC(),
	}
	if err := Save(st); err != nil {
		return State{}, err
	}

	mode := o.Render
	if mode == "" {
		mode = render.Auto()
	}
	if err := render.Attach(ctx, mode, name, wtPath, h.AttachArgv); err != nil {
		return st, err
	}

	// The egress proxy lives in this process. Terminal mode blocks inside
	// Attach for the container's lifetime, so the proxy survives. cmux mode
	// returns immediately, so saddle must stay up or the session would lose
	// its only route out mid-task.
	if mode == render.ModeCmux {
		if err := waitForExit(ctx, h.ID); err != nil {
			return st, err
		}
	}
	if px != nil {
		px.Close()
	}
	ok = true
	return st, nil
}

// waitForExit blocks until the container has appeared and then, in a second
// phase, until it is no longer listed as running.
//
// Container creation only creates the container; in cmux mode render.Attach
// returns as soon as the cmux workspace command exits, and the shell cmux
// spawned starts the container afterwards. Without a wait-to-appear phase, a
// slow cmux/shell startup or image pull could make the first poll below find
// the container absent and mistake "not started yet" for "already exited",
// closing the only egress route out while the session still has its whole
// life ahead of it.
func waitForExit(ctx context.Context, id string) error {
	deadline := time.Now().Add(120 * time.Second)
	for {
		alive, err := runtime.Running(ctx)
		if err != nil {
			return err
		}
		if alive[id] {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("container %s never started", id)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		alive, err := runtime.Running(ctx)
		if err != nil {
			return err
		}
		if !alive[id] {
			return nil
		}
	}
}

// Down tears a session down. Containers are cattle; the worktree is only
// removed when it holds nothing you would miss.
func Down(ctx context.Context, name string, force bool) error {
	st, err := Load(name)
	if err != nil {
		return err
	}

	// A worktree removed by hand is not dirty, it is just gone; do not let
	// its absence block teardown of everything else.
	if _, statErr := os.Stat(st.Worktree); statErr == nil {
		dirty, err := worktree.IsDirty(ctx, st.Worktree)
		if err != nil && !force {
			return err
		}
		if dirty && !force {
			return fmt.Errorf("worktree %s has uncommitted changes; commit them or pass --force", st.Worktree)
		}
	}

	// Best-effort remove, but then verify: if the container is genuinely
	// still running, deleting the network and state now would strand a live
	// container on an orphaned network with no record of either.
	_ = runtime.Remove(ctx, runtime.Handle{ID: st.Container})
	alive, err := runtime.Running(ctx)
	if err != nil {
		return err
	}
	if alive[st.Container] {
		return fmt.Errorf("container %s could not be removed", st.Container)
	}

	_ = runtime.DeleteNetwork(ctx, st.Network)
	if err := worktree.Remove(ctx, st.Repo, st.Worktree); err != nil && !force {
		return err
	}

	// st.Worktree is <sessDir>/worktree; remove the whole session directory
	// (mcp.json, the now-empty worktree slot) so it does not accumulate.
	_ = os.RemoveAll(filepath.Dir(st.Worktree))

	return Delete(name)
}
