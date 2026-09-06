// Package hostsvc runs host-side child processes on behalf of a session.
//
// Unlike the egress proxy, which is a goroutine and therefore cannot outlive
// the process, a child process is reparented and keeps running. So it is
// killed explicitly when the session ends, and its pid is written down so a
// later `saddle down` can reap a survivor of a crashed `up`. Killing by
// recorded pid is guarded by a command match, because a pid alone is reused.
package hostsvc

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Proc is a running child.
type Proc struct {
	PID int
	Cmd []string

	cmd *exec.Cmd
}

// Start runs argv as a child process.
func Start(argv []string) (*Proc, error) {
	if len(argv) == 0 {
		return nil, errors.New("hostsvc: empty command")
	}
	c := exec.Command(argv[0], argv[1:]...)
	// A child in its own process group is not hit by a Ctrl-C delivered to
	// saddle's group, so teardown stays under our control rather than racing
	// a signal we did not send.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("hostsvc: start %s: %w", argv[0], err)
	}
	return &Proc{PID: c.Process.Pid, Cmd: append([]string(nil), argv...), cmd: c}, nil
}

// Kill terminates the child and reaps it, so it does not linger as a zombie.
func (p *Proc) Kill() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	_, _ = p.cmd.Process.Wait()
	return nil
}

// AddrFromURL extracts the host:port a URL points at.
func AddrFromURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("hostsvc: parse %q: %w", raw, err)
	}
	if u.Port() == "" {
		// Readiness needs a concrete port. Falling back to the scheme's
		// default would poll a socket nobody bound and call it ready.
		return "", fmt.Errorf("hostsvc: %q has no port", raw)
	}
	return net.JoinHostPort(u.Hostname(), u.Port()), nil
}

// WaitReady polls until addr accepts a TCP connection or timeout expires.
func WaitReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("hostsvc: nothing listening on %s after %s", addr, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Reap kills pid, but only if its command line still matches cmd.
//
// A pid on its own is not identity: pids are reused, and Down acts on a state
// file that may be old. Without this check, tearing down a stale session
// could kill an unrelated process of the user's.
//
// The identity check compares ps's rendering of the live process's command
// line against strings.Join(cmd, " "), and that join is not injective over
// argv: []string{"foo", "a b"} and []string{"foo a", "b"} both render as
// "foo a b", so a maliciously or accidentally reshaped argv could in
// principle collide with the recorded one. The failure direction is safe
// either way: if ps errors (pid gone, or any other failure reading it), Reap
// returns nil without killing anything.
//
// A pid that is already gone is not an error; that is the ordinary case.
func Reap(pid int, cmd []string) error {
	if pid <= 0 {
		return nil
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return nil // no such process
	}
	running := strings.TrimSpace(string(out))
	if running == "" {
		return nil
	}
	if running != strings.Join(cmd, " ") {
		return fmt.Errorf("hostsvc: pid %d is now %q, not %q; refusing to kill",
			pid, running, strings.Join(cmd, " "))
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	// Reap does not require that the caller be pid's parent - ordinarily it
	// is not, since Down runs in a different process than the one that
	// spawned the child, and the kernel reparents pid to init, which reaps
	// it. But if the caller does happen to be the parent (as in a test that
	// calls Start and Reap in the same process), a blocking wait4 here could
	// hang forever on a process stuck in uninterruptible sleep, where
	// SIGKILL does not take effect immediately. So poll non-blockingly for a
	// bounded number of short waits instead of waiting unboundedly: this
	// collects the child promptly in the common in-process case without
	// ever risking hanging saddle down.
	var ws syscall.WaitStatus
	for i := 0; i < 20; i++ {
		wpid, werr := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		if werr != nil || wpid != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}
