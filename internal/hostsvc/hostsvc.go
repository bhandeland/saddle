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
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Proc is a running child.
//
// Cmd is the argv we asked for; Line is the command line the kernel actually
// ended up with, as ps renders it. The two differ whenever something rewrites
// argv, and the case that matters here is the shebang: exec'ing a script
// drops the caller's argv[0] and prepends the interpreter, so a script
// started as "remem serve --http" shows up as
// ".../Python .../remem serve --http". Recording only the argv we asked for
// would mean the identity check in Reap could never match a script.
type Proc struct {
	PID  int
	Cmd  []string
	Line string

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
	return &Proc{
		PID:  c.Process.Pid,
		Cmd:  append([]string(nil), argv...),
		Line: observeLine(c.Process.Pid),
		cmd:  c,
	}, nil
}

// psLine returns the command line ps reports for pid, or "" if it cannot be
// read (the usual reason being that pid is gone).
func psLine(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// observeLine reads the command line the child settled on, or "" if it never
// became readable.
//
// This races the exec. Between fork and exec the child is a copy of us and
// still shows *our* command line, so a reading equal to our own is treated as
// "not yet" and retried. The wait is bounded and short: a failure to observe
// falls back to the argv we asked for rather than failing the spawn, because
// a child that started is worth more than a perfect record of it.
func observeLine(pid int) string {
	self := psLine(os.Getpid())
	for i := 0; i < 20; i++ {
		if line := psLine(pid); line != "" && line != self {
			return line
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ""
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
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("hostsvc: nothing listening on %s after %s", addr, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// WaitBindable polls until this process can bind host, or timeout expires.
//
// It exists because Apple container plumbs a network's gateway address onto
// the host only while a container on that network is running: `container
// network create` reports an ipv4Gateway that is not yet on any interface,
// and the address appears a beat after `container start` returns. Callers
// need to wait for the address rather than assume it, and the delay is real
// but not a constant worth guessing at.
func WaitBindable(host string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err == nil {
			_ = ln.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("hostsvc: %s never became bindable within %s", host, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Reap kills pid, but only if its command line still identifies it as the
// child we started.
//
// A pid on its own is not identity: pids are reused, and Down acts on a state
// file that may be old. Without this check, tearing down a stale session
// could kill an unrelated process of the user's.
//
// The check compares ps's rendering of the live process's command line
// against two accepted forms: line, the rendering observed at Start (empty
// when it could not be read), and strings.Join(argv, " "). Either counts.
// That join is not injective over argv - []string{"foo", "a b"} and
// []string{"foo a", "b"} both render as "foo a b" - so a reshaped argv could
// in principle collide with the recorded one.
//
// Neither a pid that is gone nor a pid that no longer matches is an error.
// Both are evidence that our child is not there any more, which is the
// ordinary outcome of teardown; reporting them as failures would strand
// `saddle down` mid-teardown and push the user onto --force, which also
// bypasses the uncommitted-changes guard. A mismatch is worth a word on
// stderr, though, since it means a recorded pid now belongs to someone else.
// A non-nil error here means we tried to act and could not.
func Reap(pid int, argv []string, line string) error {
	if pid <= 0 {
		return nil
	}
	running := psLine(pid)
	if running == "" {
		return nil // no such process
	}
	if running != line && running != strings.Join(argv, " ") {
		fmt.Fprintf(os.Stderr, "saddle: pid %d is now %q, not %q; leaving it alone\n",
			pid, running, strings.Join(argv, " "))
		return nil
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
