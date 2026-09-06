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

type State struct {
	Name      string    `json:"name"`
	Profile   string    `json:"profile"`
	Repo      string    `json:"repo"`
	Worktree  string    `json:"worktree"`
	Branch    string    `json:"branch"`
	Network   string    `json:"network"`
	Container string    `json:"container"`
	ProxyAddr string    `json:"proxy_addr"`
	Status    string    `json:"status"`
	Created   time.Time `json:"created"`
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
			continue // a corrupt file must not hide healthy sessions
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
