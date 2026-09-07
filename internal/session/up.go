package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brandon/saddle/internal/auth"
	"github.com/brandon/saddle/internal/config"
	"github.com/brandon/saddle/internal/egress"
	"github.com/brandon/saddle/internal/hostsvc"
	"github.com/brandon/saddle/internal/macos"
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

// SpawnReadyTimeout bounds how long a spawned host service has to bind.
// A var, not a const, so tests need not wait it out.
var SpawnReadyTimeout = 30 * time.Second

// GatewayBindTimeout bounds the wait for a freshly started anchor to put the
// session network's gateway address onto the host. Generous on purpose: the
// alternative to waiting is a bind error the operator cannot act on.
const GatewayBindTimeout = 30 * time.Second

// startSpawned starts every carried-in MCP server that declares a spawn
// command, and does not return until each is accepting connections.
//
// Waiting is the point. The egress proxy binds synchronously, so nothing can
// race it; a child process does not, and a container started before the
// server binds gives the agent an MCP server that does not exist. A session
// that looks contained and memory-backed but is only the first of those is
// exactly what saddle refuses to produce, so a server that never binds fails
// `up` rather than being shrugged off.
//
// The profile must already have been expanded: these arguments carry the
// session gateway.
func startSpawned(p profile.Profile, timeout time.Duration) ([]*hostsvc.Proc, []Spawned, error) {
	var procs []*hostsvc.Proc
	var rec []Spawned

	fail := func(err error) ([]*hostsvc.Proc, []Spawned, error) {
		for _, pr := range procs {
			_ = pr.Kill()
		}
		return nil, nil, err
	}

	// Map iteration order is random; sort so a failure is reproducible and
	// two runs of the same profile start servers in the same order.
	names := make([]string, 0, len(p.CarryIn.MCP))
	for name := range p.CarryIn.MCP {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		m := p.CarryIn.MCP[name]
		if len(m.Spawn) == 0 {
			continue
		}
		addr, err := hostsvc.AddrFromURL(m.URL)
		if err != nil {
			return fail(fmt.Errorf("carry_in.mcp.%s: %w", name, err))
		}
		pr, err := hostsvc.Start(m.Spawn)
		if err != nil {
			return fail(fmt.Errorf("carry_in.mcp.%s: %w", name, err))
		}
		procs = append(procs, pr)
		if err := hostsvc.WaitReady(addr, timeout); err != nil {
			return fail(fmt.Errorf("carry_in.mcp.%s: %w", name, err))
		}
		rec = append(rec, Spawned{Name: name, PID: pr.PID, Cmd: pr.Cmd, Line: pr.Line, Addr: addr})
	}
	return procs, rec, nil
}

// Up creates a contained session and attaches to it.
func Up(ctx context.Context, o UpOptions) (State, error) {
	if o.NoNet && o.OpenNet {
		return State{}, fmt.Errorf("--no-net and --open-net are mutually exclusive")
	}

	// Resolve the target once, up front. `saddle up` defaults to ".", and
	// every later use of o.Repo takes a name from it - the session name, and
	// the {{repo}} the profile hands the agent as a memory project. Left
	// relative, both come out as "." and the session records its work under a
	// project literally named ".", which no knowledge base ever queries.
	if abs, err := filepath.Abs(o.Repo); err == nil {
		o.Repo = abs
	}

	// The containment rests on `--internal` network behaviour that was only
	// ever verified on macOS 26. Below the floor saddle would be running an
	// agent with permission prompting disabled behind isolation nobody has
	// checked, so refuse rather than guess.
	if _, err := macos.CheckFloor(); err != nil {
		return State{}, err
	}

	// Resources are acquired one at a time below. ok is disarmed (set true)
	// as soon as Save persists the session, which is the point at which
	// `saddle down` becomes able to find and tear down everything created so
	// far. Before that point, an early return here would otherwise orphan a
	// worktree, network, proxy, or container with no state file pointing at
	// it, so this defer unwinds anything actually created, in reverse order.
	// It must never fire after Save has succeeded: doing so would delete a
	// live, recorded session's worktree (including uncommitted work) out
	// from under it on any later failure, such as a Ctrl-C during attach.
	var (
		sessDirCreated bool
		sessDir        string
		wtCreated      bool
		wtPath         string
		netCreated     bool
		netName        string
		anchorCreated  bool
		anchorName     string
		px             *egress.Proxy
		spawned        []*hostsvc.Proc
		containerID    string
	)
	ok := false
	defer func() {
		if ok {
			return
		}
		// Cleanup must not use the caller's ctx: on cancellation (e.g. a
		// Ctrl-C that also triggered this failure) exec.CommandContext calls
		// below would fail instantly and their errors are discarded, so
		// the container and network would survive while px.Close() and
		// os.RemoveAll (which are not ctx-bound) would still happen —
		// leaving a live container with its /work mount deleted and no
		// egress route out. Give teardown its own bounded time instead.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if containerID != "" {
			_ = runtime.Remove(cleanupCtx, runtime.Handle{ID: containerID})
		}
		for _, pr := range spawned {
			_ = pr.Kill()
		}
		if px != nil {
			_ = px.Close()
		}
		// After the proxy and spawned services, which are bound to the
		// gateway this anchor is holding up, and before DeleteNetwork, which
		// cannot run while a container is still on the network. A failure
		// here is reported and stepped over: aborting teardown now would
		// leave a half-removed session that only --force can clear, and
		// --force also skips the uncommitted-changes guard.
		if anchorCreated {
			if err := runtime.Remove(cleanupCtx, runtime.Handle{ID: anchorName}); err != nil {
				fmt.Fprintf(os.Stderr, "saddle: could not remove anchor %s: %v\n", anchorName, err)
			}
		}
		if netCreated {
			_ = runtime.DeleteNetwork(cleanupCtx, netName)
		}
		if wtCreated {
			_ = worktree.Remove(cleanupCtx, o.Repo, wtPath)
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
	sessDir, err = claimSessionDir(stateDir, name)
	if err != nil {
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

	// 4b. Anchor. Apple container puts the gateway address on the host only
	// while a container on the network is running, so nothing below can bind
	// it until something is up. See
	// docs/superpowers/specs/2026-09-06-session-network-anchor-design.md.
	anchorName = AnchorName(name)
	if _, err := runtime.Create(ctx, AnchorSpec(name, p.Image, netName)); err != nil {
		return State{}, fmt.Errorf("create anchor: %w", err)
	}
	anchorCreated = true
	if err := runtime.Start(ctx, anchorName); err != nil {
		return State{}, fmt.Errorf("start anchor: %w", err)
	}

	// 4c. The address appears a beat after the start returns, so wait for it
	// rather than assuming it.
	if err := hostsvc.WaitBindable(n.Gateway, GatewayBindTimeout); err != nil {
		return State{}, fmt.Errorf("session gateway never came up: %w", err)
	}

	// 5. Egress proxy bound to the gateway. --open-net means no filtering at
	// all, so no proxy is started: a listening proxy nobody uses is just a
	// stray port and a leaked goroutine.
	allow := append([]string{}, p.Egress.Allow...)
	allow = append(allow, o.ExtraAllow...)
	if o.NoNet || o.OpenNet {
		allow = nil
	}
	var proxyAddr, proxyPort string
	if !o.OpenNet {
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
		"repo":       worktree.RepoName(ctx, o.Repo),
	})

	// 6b. Host services the profile asks for, before the container so that
	// nothing races their bind. See startSpawned for why this waits.
	//
	// `spawned` is the ladder variable declared above, so this assigns
	// rather than declares: a `:=` here would shadow it and the unwind
	// would kill nothing.
	var spawnedRec []Spawned
	spawned, spawnedRec, err = startSpawned(p, SpawnReadyTimeout)
	if err != nil {
		return State{}, err
	}

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

	// 10. Persist before attaching. This is the point of no return for the
	// unwind above: once the session is recorded, `saddle down` owns its
	// teardown, so a failure past this point must be returned as-is rather
	// than have this function delete the very session it just recorded —
	// that would destroy a live, possibly-dirty worktree out from under an
	// attach failure or a Ctrl-C instead of leaving a recoverable session.
	st := State{
		Name: name, Profile: p.Name, Repo: o.Repo, Worktree: wtPath,
		Branch: "saddle/" + branch, Network: netName, Container: h.ID,
		Anchor:    anchorName,
		ProxyAddr: proxyAddr, Spawned: spawnedRec,
		Egress: egressSummary(o.OpenNet, o.NoNet, allow), Allow: allow,
		Status: "running", Created: time.Now().UTC(),
	}
	if err := Save(st); err != nil {
		return State{}, err
	}
	ok = true

	// The unwind above is disarmed, so nothing else will close the proxy or
	// kill spawned host services on an early return from here down. Their
	// lifetime should not rest on the process exiting.
	defer func() {
		if px != nil {
			_ = px.Close()
		}
		for _, pr := range spawned {
			_ = pr.Kill()
		}
	}()

	mode := o.Render
	if mode == "" {
		mode = render.Auto()
	}

	// Tell the operator what was actually decided. Without this the
	// generated session name is never shown (so `saddle down` has no
	// argument to be given), and a profile skipped for being malformed —
	// which only warns on stderr — silently puts a different profile's
	// allowlist in force with nothing to correlate against.
	printSummary(os.Stdout, st, p, o.ProfileName != "", allow, mode)

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
	return st, nil
}

// printSummary writes the post-creation summary described by the design
// spec: what was resolved, where the work lives, and how much of the
// internet the session can reach.
func printSummary(w io.Writer, st State, p profile.Profile, named bool, allow []string, mode render.Mode) {
	how := "detected"
	if named {
		how = "named"
	}
	egressLine := strings.Join(allow, ", ") + "  (all else denied)"
	switch st.Egress {
	case "open":
		egressLine = "OPEN - no filtering at all"
	case "none":
		egressLine = "none  (all egress denied)"
	}
	_, _ = fmt.Fprintf(w, "  session   %s\n", st.Name)
	_, _ = fmt.Fprintf(w, "  profile   %s (%s)\n", p.Name, how)
	_, _ = fmt.Fprintf(w, "  worktree  %s  [branch %s]\n", st.Worktree, st.Branch)
	_, _ = fmt.Fprintf(w, "  egress    %s\n", egressLine)
	_, _ = fmt.Fprintf(w, "  attach    %s\n", mode)
}

// pendingStart reports whether a status means "this container has not run
// yet", as opposed to "it ran and finished".
//
// Apple container has no distinct created state: a container that exists but
// has never been started reports "stopped", the same string a container that
// ran and exited reports. The two are indistinguishable from the status
// alone, so both are treated as pending and the caller's deadline is what
// separates them - a container that never starts fails when the deadline
// expires.
func pendingStart(status string) bool {
	return status == "created" || status == "stopped"
}

// waitForExit blocks until the container has appeared as running and then,
// in a second phase, until it is no longer listed as running.
//
// Container creation only creates the container; in cmux mode render.Attach
// returns as soon as the cmux workspace command exits, and the shell cmux
// spawned starts the container afterwards. The appear phase uses
// runtime.Status rather than runtime.Running, because Running's `container
// list` (without -a) only ever lists running containers: a container that
// starts and exits within one poll window would never be observed alive,
// so absence alone cannot distinguish "hasn't started yet" from "already
// exited" — and mistaking the latter for the former would close the only
// egress route out while the session still has its whole life ahead of it.
func waitForExit(ctx context.Context, id string) error {
	deadline := time.Now().Add(120 * time.Second)
	for {
		status, err := runtime.Status(ctx, id)
		if err != nil {
			return err
		}
		switch {
		case status == "running":
		case pendingStart(status):
			// Not yet started; keep waiting, subject to the deadline below.
			if time.Now().After(deadline) {
				return fmt.Errorf("container %s never started within %s", id, 120*time.Second)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			continue
		default:
			// "" (no longer listed at all) or any other terminal state
			// means it already ran and stopped before we observed it.
			return fmt.Errorf("container %s exited before it could be observed running (status %q)", id, status)
		}
		break
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

	// Children of the `up` process. Normally already dead, because `up`
	// kills them on the way out; this catches a survivor of a crashed `up`.
	// Reap refuses to kill a pid whose command no longer matches, so a
	// recycled pid is never mistaken for our child.
	//
	// Before DeleteNetwork, matching the order Up's unwind uses: a spawned
	// server is bound to the session gateway, so deleting the network first
	// would pull that address out from under the process we are about to
	// identify.
	for _, s := range st.Spawned {
		if err := hostsvc.Reap(s.PID, s.Cmd, s.Line); err != nil && !force {
			return err
		}
	}

	_ = runtime.DeleteNetwork(ctx, st.Network)

	if err := worktree.Remove(ctx, st.Repo, st.Worktree); err != nil && !force {
		return err
	}

	// st.Worktree is normally <sessDir>/worktree; remove the whole session
	// directory (mcp.json, the now-empty worktree slot) so it does not
	// accumulate. sessionCleanupDir refuses to act on an empty or
	// out-of-place path, so a truncated or hand-edited state file can never
	// turn this into an rm -rf of something saddle does not own.
	if stateDir, err := Dir(); err == nil {
		if d, ok := sessionCleanupDir(stateDir, st.Worktree); ok {
			_ = os.RemoveAll(d)
		}
	}

	return Delete(name)
}

// claimSessionDir creates the per-session directory under stateDir and
// returns its path. It uses os.Mkdir, not os.MkdirAll: MkdirAll succeeds on
// a directory that already exists, which would let a colliding --name adopt
// a live session's directory and then delete it — worktree and all — when
// the caller's cleanup unwind fires on the next failure. An existing
// directory is therefore a hard error, raised before anything is acquired.
func claimSessionDir(stateDir, name string) (string, error) {
	dir := filepath.Join(stateDir, name+".d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("session %q already exists; pick another --name or run `saddle down %s`", name, name)
		}
		return "", err
	}
	return dir, nil
}

// egressSummary describes a session's containment posture in a few
// characters, for the state file and the `saddle ls` table. It is short on
// purpose: an operator with several sessions must be able to answer "which
// of these is uncontained?" at a glance.
func egressSummary(openNet, noNet bool, allow []string) string {
	switch {
	case openNet:
		return "open"
	case noNet || len(allow) == 0:
		return "none"
	case len(allow) == 1:
		return allow[0]
	default:
		return fmt.Sprintf("%s +%d", allow[0], len(allow)-1)
	}
}

// sessionCleanupDir returns the session directory to remove for a session's
// worktree path, and whether removing it is safe. An empty worktree path is
// refused outright: filepath.Dir("") is ".", and blindly removing that
// would delete the current working directory. The candidate directory must
// also fall STRICTLY under saddle's state directory, so nothing outside
// saddle's own control is ever touched. Equality with the state directory is
// refused outright: a session directory is always one level below it, so a
// candidate equal to it means the worktree path was malformed, and removing
// it would wipe every other session's directory too.
func sessionCleanupDir(stateDir, worktreePath string) (string, bool) {
	if worktreePath == "" {
		return "", false
	}
	dir := filepath.Clean(filepath.Dir(worktreePath))
	base := filepath.Clean(stateDir)
	if !strings.HasPrefix(dir, base+string(filepath.Separator)) {
		return "", false
	}
	return dir, true
}
