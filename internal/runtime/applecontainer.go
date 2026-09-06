// Package runtime is the only package that knows Apple `container` exists.
// Nothing else in saddle may invoke the container CLI. When a second backend
// becomes real, an interface is extracted from two implementations rather
// than predicted from one.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

type Network struct {
	Name    string
	Gateway string
}

type Spec struct {
	Name    string
	Image   string
	Network string
	Workdir string
	Cmd     []string
	Env     map[string]string
	Mounts  []Mount
	CPUs    int
	Memory  string
}

type Handle struct {
	ID         string
	AttachArgv []string
}

func run(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "container", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("container %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// Preflight verifies the container service is reachable.
func Preflight(ctx context.Context) error {
	_, err := run(ctx, "system", "status")
	return err
}

type inspectNetwork struct {
	ID     string `json:"id"`
	Status struct {
		IPv4Gateway string `json:"ipv4Gateway"`
	} `json:"status"`
}

func parseGateway(data []byte) (string, error) {
	var nets []inspectNetwork
	if err := json.Unmarshal(data, &nets); err != nil {
		return "", fmt.Errorf("parse network inspect: %w", err)
	}
	if len(nets) == 0 {
		return "", errors.New("network inspect returned no networks")
	}
	if nets[0].Status.IPv4Gateway == "" {
		return "", errors.New("network has no ipv4Gateway")
	}
	return nets[0].Status.IPv4Gateway, nil
}

// CreateNetwork makes a network and returns its discovered gateway. When
// isolated is true the network has no route to the internet, but the host
// remains reachable at the gateway.
func CreateNetwork(ctx context.Context, name string, isolated bool) (Network, error) {
	args := []string{"network", "create"}
	if isolated {
		args = append(args, "--internal")
	}
	if _, err := run(ctx, append(args, name)...); err != nil {
		return Network{}, err
	}
	out, err := run(ctx, "network", "inspect", name)
	if err != nil {
		return Network{}, err
	}
	gw, err := parseGateway([]byte(out))
	if err != nil {
		return Network{}, err
	}
	return Network{Name: name, Gateway: gw}, nil
}

func DeleteNetwork(ctx context.Context, name string) error {
	_, err := run(ctx, "network", "delete", name)
	return err
}

func createArgs(s Spec) []string {
	args := []string{"create", "--name", s.Name}
	if s.Network != "" {
		args = append(args, "--network", s.Network)
	}
	for _, m := range s.Mounts {
		v := m.Source + ":" + m.Target
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "--volume", v)
	}
	if s.Workdir != "" {
		args = append(args, "--workdir", s.Workdir)
	}
	// Sorted for deterministic argv, which keeps tests stable.
	for _, k := range sortedKeys(s.Env) {
		args = append(args, "--env", k+"="+s.Env[k])
	}
	if s.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(s.CPUs))
	}
	if s.Memory != "" {
		args = append(args, "--memory", s.Memory)
	}
	args = append(args, s.Image)
	return append(args, s.Cmd...)
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j] < ks[j-1]; j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
	return ks
}

// Create makes the container without starting it, and returns the argv that
// attaches a TTY to it. Returning the attach argv is what lets render stay
// backend-agnostic.
func Create(ctx context.Context, s Spec) (Handle, error) {
	if _, err := run(ctx, createArgs(s)...); err != nil {
		return Handle{}, err
	}
	return Handle{
		ID:         s.Name,
		AttachArgv: []string{"container", "start", "-ai", s.Name},
	}, nil
}

func Remove(ctx context.Context, h Handle) error {
	_, err := run(ctx, "rm", "-f", h.ID)
	return err
}

func parseRunning(out string) map[string]bool {
	alive := map[string]bool{}
	for i, line := range strings.Split(out, "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue // header or blank
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			alive[fields[0]] = true
		}
	}
	return alive
}

// Running reports which containers are currently alive. It backs both `saddle
// ls` reconciliation and the wait in cmux mode.
func Running(ctx context.Context) (map[string]bool, error) {
	out, err := run(ctx, "list")
	if err != nil {
		return nil, err
	}
	return parseRunning(out), nil
}
