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

// shellQuote renders s as a single POSIX shell word. cmux's --command value is
// typed into an interactive shell, so every element must survive shell parsing
// as exactly one argument.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// CmuxArgs builds the cmux invocation that opens a workspace running argv.
func CmuxArgs(name, cwd string, argv []string) []string {
	var quoted []string
	for _, arg := range argv {
		quoted = append(quoted, shellQuote(arg))
	}
	return []string{
		"new-workspace",
		"--name", name,
		"--cwd", cwd,
		"--command", strings.Join(quoted, " "),
	}
}

// Attach runs the session. In terminal mode saddle inherits stdio and the
// container allocates the TTY, so no PTY library is required. Terminal mode
// blocks until the session exits and propagates the exit status; cmux mode
// returns immediately when the workspace is created and cannot report whether
// the session inside succeeded.
func Attach(ctx context.Context, m Mode, name, cwd string, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("render: empty argv")
	}
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
