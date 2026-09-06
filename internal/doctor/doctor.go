// Package doctor turns saddle's several confusing setup failures into one
// checklist with fixes.
package doctor

import (
	"context"
	"os/exec"
	"strings"
)

type Result struct {
	Name   string
	OK     bool
	Detail string
	Fix    string
}

// ParseListeners returns "command addr" for every TCP listener bound to a
// non-loopback address. Those are reachable from a saddle session, because
// traffic to the host gateway does not traverse the egress proxy.
func ParseListeners(lsofOutput string) []string {
	var out []string
	for i, line := range strings.Split(lsofOutput, "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue // header or blank
		}
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		addr := fields[8]
		if strings.HasPrefix(addr, "127.0.0.1:") || strings.HasPrefix(addr, "[::1]:") {
			continue
		}
		out = append(out, fields[0]+" "+addr)
	}
	return out
}

func Run(ctx context.Context) []Result {
	var rs []Result

	if _, err := exec.LookPath("container"); err != nil {
		rs = append(rs, Result{Name: "container installed", OK: false,
			Detail: "container is not on PATH", Fix: "brew install container"})
		return rs // everything else depends on this
	}
	rs = append(rs, Result{Name: "container installed", OK: true})

	if out, err := exec.CommandContext(ctx, "container", "system", "status").CombinedOutput(); err != nil {
		rs = append(rs, Result{Name: "container service", OK: false,
			Detail: strings.TrimSpace(string(out)), Fix: "container system start"})
	} else {
		rs = append(rs, Result{Name: "container service", OK: true})
	}

	// A canary run is the only reliable check that a kernel is configured;
	// the missing-kernel failure only surfaces when starting a container.
	canary := exec.CommandContext(ctx, "container", "run", "--rm",
		"docker.io/library/alpine:3.20", "true")
	if out, err := canary.CombinedOutput(); err != nil {
		rs = append(rs, Result{Name: "can run containers", OK: false,
			Detail: strings.TrimSpace(string(out)),
			Fix:    "container system kernel set --recommended"})
	} else {
		rs = append(rs, Result{Name: "can run containers", OK: true})
	}

	tok := exec.CommandContext(ctx, "security", "find-generic-password",
		"-s", "saddle-claude-token", "-w")
	if err := tok.Run(); err != nil {
		rs = append(rs, Result{Name: "claude token", OK: false,
			Detail: "no saddle-owned token in the keychain", Fix: "saddle auth"})
	} else {
		rs = append(rs, Result{Name: "claude token", OK: true})
	}

	lsof := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN")
	if out, err := lsof.Output(); err == nil {
		if exposed := ParseListeners(string(out)); len(exposed) > 0 {
			rs = append(rs, Result{Name: "host services exposed", OK: false,
				Detail: "reachable from saddle sessions: " + strings.Join(exposed, ", "),
				Fix:    "bind these to 127.0.0.1, or accept that sessions can reach them"})
		} else {
			rs = append(rs, Result{Name: "host services exposed", OK: true})
		}
	}

	return rs
}
