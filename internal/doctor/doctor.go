// Package doctor turns saddle's several confusing setup failures into one
// checklist with fixes.
package doctor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/brandon/saddle/internal/macos"
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

// decideCanaryAndListenerResults returns the Results for "can run containers"
// (if service is not OK) and "host services exposed" checks based on whether
// the container service is running and the lsof output or error.
func decideCanaryAndListenerResults(serviceOK bool, lsofErr error, lsofOutput string) []Result {
	var rs []Result

	if !serviceOK {
		rs = append(rs, Result{Name: "can run containers", OK: false,
			Detail: "skipped: the container service is not running",
			Fix:    "container system start, then re-run saddle doctor"})
	}

	if lsofErr != nil {
		rs = append(rs, Result{Name: "host services exposed", OK: false,
			Detail: "could not enumerate listeners: " + lsofErr.Error(),
			Fix:    "ensure lsof is available, or check manually with: lsof -nP -iTCP -sTCP:LISTEN"})
	} else {
		if exposed := ParseListeners(lsofOutput); len(exposed) > 0 {
			rs = append(rs, Result{Name: "host services exposed", OK: false,
				Detail: "reachable from saddle sessions: " + strings.Join(exposed, ", "),
				Fix:    "bind these to 127.0.0.1, or accept that sessions can reach them"})
		} else {
			rs = append(rs, Result{Name: "host services exposed", OK: true})
		}
	}

	return rs
}

func Run(ctx context.Context) []Result {
	var rs []Result

	// The macOS floor is a containment claim, not a packaging preference:
	// below it, `--internal` isolation is unverified. Check it here so a
	// `go build` install is told, not just a Homebrew one.
	if v, err := macos.CheckFloor(); err != nil {
		rs = append(rs, Result{Name: "macOS version", OK: false,
			Detail: err.Error(),
			Fix:    fmt.Sprintf("upgrade to macOS %d (Tahoe) or newer; saddle's egress containment was only verified there", macos.Floor)})
	} else {
		rs = append(rs, Result{Name: "macOS version", OK: true, Detail: v})
	}

	if _, err := exec.LookPath("container"); err != nil {
		rs = append(rs, Result{Name: "container installed", OK: false,
			Detail: "container is not on PATH", Fix: "brew install container"})
		return rs // everything else depends on this
	}
	rs = append(rs, Result{Name: "container installed", OK: true})

	var serviceOK bool
	if out, err := exec.CommandContext(ctx, "container", "system", "status").CombinedOutput(); err != nil {
		rs = append(rs, Result{Name: "container service", OK: false,
			Detail: strings.TrimSpace(string(out)), Fix: "container system start"})
		serviceOK = false
	} else {
		rs = append(rs, Result{Name: "container service", OK: true})
		serviceOK = true
	}

	// A canary run is the only reliable check that a kernel is configured;
	// the missing-kernel failure only surfaces when starting a container.
	if serviceOK {
		canary := exec.CommandContext(ctx, "container", "run", "--rm",
			"docker.io/library/alpine:3.20", "true")
		if out, err := canary.CombinedOutput(); err != nil {
			rs = append(rs, Result{Name: "can run containers", OK: false,
				Detail: strings.TrimSpace(string(out)),
				Fix:    "container system kernel set --recommended"})
		} else {
			rs = append(rs, Result{Name: "can run containers", OK: true})
		}
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
	out, lsofErr := lsof.Output()
	rs = append(rs, decideCanaryAndListenerResults(serviceOK, lsofErr, string(out))...)

	return rs
}
