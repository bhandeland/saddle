package session

import (
	"context"
	"fmt"
	"net"

	"github.com/brandon/saddle/internal/egress"
	"github.com/brandon/saddle/internal/hostsvc"
	"github.com/brandon/saddle/internal/render"
	"github.com/brandon/saddle/internal/runtime"
)

// proxyPortFree reports whether addr can be bound, so attach can refuse
// rather than race another saddle process for the same session.
func proxyPortFree(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("egress proxy address %s is already in use; another saddle process is probably already attached to this session", addr)
	}
	return ln.Close()
}

// Attach re-enters an existing session.
//
// The host side of a session belongs to whichever saddle process is attached:
// `up` binds the egress proxy and starts the carried-in servers, and kills
// them when it exits. So a session re-entered later has no proxy and no
// memory server, and the symptoms are misleading - egress fails closed and
// looks like the network is down, MCP calls error against a dead endpoint.
//
// Rebuilding that host side is only possible because the anchor kept the
// gateway address on the host after the session container stopped. The proxy
// must come back on *exactly* the recorded address: the container's
// HTTPS_PROXY was baked in when it was created and still points there.
func Attach(ctx context.Context, name string, mode render.Mode) error {
	st, err := Load(name)
	if err != nil {
		return err
	}

	// 1. The anchor, so the gateway address exists before anything binds it.
	if st.Anchor != "" {
		if err := runtime.Start(ctx, st.Anchor); err != nil {
			return fmt.Errorf("start anchor %s: %w (was it removed? tear the session down with `saddle down %s`)", st.Anchor, err, name)
		}
	}

	// 2. The egress proxy, at the address the container already believes in.
	var px *egress.Proxy
	if st.ProxyAddr != "" {
		host, _, splitErr := net.SplitHostPort(st.ProxyAddr)
		if splitErr != nil {
			return fmt.Errorf("state file records an unusable proxy address %q: %w", st.ProxyAddr, splitErr)
		}
		if err := hostsvc.WaitBindable(host, GatewayBindTimeout); err != nil {
			return fmt.Errorf("session gateway never came up: %w", err)
		}
		if err := proxyPortFree(st.ProxyAddr); err != nil {
			return err
		}
		px = egress.New(st.Allow)
		if _, err := px.Listen(st.ProxyAddr); err != nil {
			return fmt.Errorf("rebind egress proxy on %s: %w", st.ProxyAddr, err)
		}
		defer func() { _ = px.Close() }()
	}

	// 3. The carried-in servers, from the argv recorded when they last ran.
	var procs []*hostsvc.Proc
	defer func() {
		for _, pr := range procs {
			_ = pr.Kill()
		}
	}()
	for _, s := range st.Spawned {
		if len(s.Cmd) == 0 {
			continue
		}
		pr, err := hostsvc.Start(s.Cmd)
		if err != nil {
			return fmt.Errorf("restart %s: %w", s.Name, err)
		}
		procs = append(procs, pr)
		if s.Addr == "" {
			continue // recorded before addresses were, so nothing to wait on
		}
		if err := hostsvc.WaitReady(s.Addr, SpawnReadyTimeout); err != nil {
			return fmt.Errorf("restart %s: %w", s.Name, err)
		}
	}

	if err := render.Attach(ctx, mode, st.Name, st.Worktree, runtime.AttachArgv(st.Container)); err != nil {
		return err
	}

	// The proxy and spawned servers just rebuilt above live in this process,
	// so they die the moment Attach returns. Terminal mode blocks inside
	// render.Attach for the container's lifetime, so they survive. cmux mode
	// returns as soon as the workspace is created, before the session has
	// even started using them - without this wait the deferred cleanup above
	// would tear the host side back down immediately after rebuilding it.
	if mode == render.ModeCmux {
		if err := waitForExit(ctx, st.Container); err != nil {
			return err
		}
	}
	return nil
}
