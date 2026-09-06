// Package session owns saddle's file-backed state. State is files, never
// in-memory only, so a crashed saddle or a rebooted machine still produces
// accurate output.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Spawned records a host-side child process started for this session, so a
// later `saddle down` - a different process from the `up` that started it -
// can reap a survivor of a crashed `up`. Cmd is recorded alongside Pid
// because a pid on its own is reused and is not identity.
//
// Line is the command line the child actually ended up with, which is not
// always Cmd joined: exec'ing a shebang script drops argv[0] and prepends the
// interpreter. It is what the identity check matches on. Older state files
// have no Line; Cmd still matches for anything that is not a script.
type Spawned struct {
	Name string   `json:"name"`
	PID  int      `json:"pid"`
	Cmd  []string `json:"cmd"`
	Line string   `json:"line,omitempty"`
}

type State struct {
	Name      string    `json:"name"`
	Profile   string    `json:"profile"`
	Repo      string    `json:"repo"`
	Worktree  string    `json:"worktree"`
	Branch    string    `json:"branch"`
	Network   string    `json:"network"`
	Container string    `json:"container"`
	ProxyAddr string    `json:"proxy_addr"`
	Spawned   []Spawned `json:"spawned"`
	// Egress records the containment posture the session was created with:
	// "open" (no filtering), "none" (all denied), or a short allowlist
	// summary. Written down rather than inferred, because the only other
	// signal is one stderr line at creation that scrolls away in cmux mode,
	// and an operator with several sessions must still be able to answer
	// "which of these is uncontained?".
	Egress  string    `json:"egress"`
	Status  string    `json:"status"`
	Created time.Time `json:"created"`
}

// EgressLabel renders a session's containment posture for a table column.
// An unrestricted session is flagged so it cannot be skimmed past, and a
// state file written before the field existed reads as "unknown" rather
// than as a reassuring blank.
func (s State) EgressLabel() string {
	switch s.Egress {
	case "":
		return "unknown"
	case "open":
		return "OPEN(!)"
	default:
		return s.Egress
	}
}

// Dir returns the sessions directory, honouring XDG_STATE_HOME so tests can
// redirect it.
func Dir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	d := filepath.Join(base, "saddle", "sessions")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

func path(name string) (string, error) {
	// Validate name to prevent path traversal attacks.
	// Reject: empty, contains path separators, equals . or .., or differs from Base.
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("invalid session name %q", name)
	}
	if filepath.Base(name) != name {
		return "", fmt.Errorf("invalid session name %q: contains path separators", name)
	}
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name+".json"), nil
}

func Save(s State) error {
	p, err := path(s.Name)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// Write then rename, so a crash mid-write cannot leave a truncated file.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func Load(name string) (State, error) {
	p, err := path(name)
	if err != nil {
		return State{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return State{}, fmt.Errorf("no such session %q: %w", name, err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("corrupt session file %s: %w", p, err)
	}
	return s, nil
}

func List() ([]State, error) {
	d, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		return nil, err
	}
	var out []State
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		s, err := Load(e.Name()[:len(e.Name())-len(".json")])
		if err != nil {
			// Skip corrupt/unreadable files but warn
			fmt.Fprintf(os.Stderr, "saddle: skipping unreadable session file %s: %v\n", e.Name(), err)
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func Delete(name string) error {
	p, err := path(name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// Reconcile marks sessions stopped when their container is no longer alive.
// Pure function: it returns a new slice and does not touch its input.
func Reconcile(states []State, alive map[string]bool) []State {
	out := make([]State, len(states))
	copy(out, states)
	for i := range out {
		if out[i].Status == "running" && !alive[out[i].Container] {
			out[i].Status = "stopped"
		}
	}
	return out
}
