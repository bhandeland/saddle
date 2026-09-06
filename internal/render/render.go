// Package render attaches the operator to a running session. It knows how to
// attach, not what it is attaching to: it receives an argv and never imports
// the runtime package.
package render

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Mode string

const (
	ModeTerminal Mode = "terminal"
	ModeCmux     Mode = "cmux"
)

// Auto picks cmux when it is installed, otherwise the current terminal.
func Auto() Mode {
	if _, err := exec.LookPath("cmux"); err == nil {
		return ModeCmux
	}
	return ModeTerminal
}

// CmuxArgs builds the cmux invocation that opens a workspace running argv.
func CmuxArgs(name, cwd string, argv []string) []string {
	return []string{
		"new-workspace",
		"--name", name,
		"--cwd", cwd,
		"--command", strings.Join(argv, " "),
	}
}

// Attach runs the session. In terminal mode saddle inherits stdio and the
// container allocates the TTY, so no PTY library is required.
func Attach(ctx context.Context, m Mode, name, cwd string, argv []string) error {
	switch m {
	case ModeCmux:
		out, err := exec.CommandContext(ctx, "cmux", CmuxArgs(name, cwd, argv)...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("cmux new-workspace: %w: %s", err, out)
		}
		return nil
	case ModeTerminal:
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	default:
		return fmt.Errorf("unknown render mode %q", m)
	}
}
