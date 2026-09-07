//go:build contract

package runtime

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const img = "docker.io/library/alpine:3.20"

// netProbeScript is the shell script both TestNetworkContract and
// TestNetworkContractPositiveControl run inside the container. Keeping it as
// a single constant guarantees the two tests differ only in network
// isolation, not in the assertion mechanism being exercised.
const netProbeScript = "wget -q -T 4 -O- http://example.com >/dev/null 2>&1 && echo INTERNET || echo NOINTERNET"

// TestUIDContract: a file written inside the container must appear on the
// host owned by the invoking user.
func TestUIDContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dir := t.TempDir()
	name := "saddle-contract-uid"
	sp := Spec{
		Name: name, Image: img,
		Mounts:  []Mount{{Source: dir, Target: "/work"}},
		Workdir: "/work",
		Cmd:     []string{"sh", "-c", "echo written > /work/out.txt"},
	}
	h, err := Create(ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	defer Remove(context.Background(), h)

	if out, err := exec.CommandContext(ctx, "container", "start", "-a", name).CombinedOutput(); err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	fi, err := os.Stat(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("file not written back to host: %v", err)
	}
	uid := fileUID(t, fi)
	if uid != os.Getuid() {
		t.Fatalf("uid contract violated: file owned by %d, want %d", uid, os.Getuid())
	}
}

// TestNetworkContract: on an isolated network there is no route out, and the
// host gateway is reachable.
func TestNetworkContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	netName := "saddle-contract-net"
	n, err := CreateNetwork(ctx, netName, true)
	if err != nil {
		t.Fatal(err)
	}
	defer DeleteNetwork(context.Background(), netName)

	if n.Gateway == "" {
		t.Fatal("isolated network reported no gateway")
	}

	name := "saddle-contract-netc"
	h, err := Create(ctx, Spec{
		Name: name, Image: img, Network: netName,
		Cmd: []string{"sh", "-c", netProbeScript},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer Remove(context.Background(), h)

	out, err := exec.CommandContext(ctx, "container", "start", "-a", name).CombinedOutput()
	if err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "NOINTERNET") {
		t.Fatalf("network contract violated: container reached the internet despite running on an isolated network; containment is broken\n%s", out)
	}
}

// TestNetworkContractPositiveControl proves the probe script used by
// TestNetworkContract can actually report INTERNET when a container is not
// isolated. Without this, TestNetworkContract's NOINTERNET result could mean
// either genuine isolation or a broken assertion mechanism (missing wget,
// bad flags, a DNS quirk) — indistinguishable from each other on their own.
func TestNetworkContractPositiveControl(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "example.com:80", 5*time.Second)
	if err != nil {
		t.Skip("host has no internet; cannot validate the positive control")
	}
	conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	name := "saddle-contract-poscontrol"
	h, err := Create(ctx, Spec{
		Name: name, Image: img,
		Cmd: []string{"sh", "-c", netProbeScript},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer Remove(context.Background(), h)

	out, err := exec.CommandContext(ctx, "container", "start", "-a", name).CombinedOutput()
	if err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "NOINTERNET") || !strings.Contains(string(out), "INTERNET") {
		t.Fatalf("positive control failed: an unisolated container reported NOINTERNET; the probe script's assertion mechanism is broken, so TestNetworkContract's pass proves nothing\n%s", out)
	}
}

// bindable reports whether this process can open a TCP listener on host.
func bindable(host string) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// TestGatewayBindableOnlyWhileAContainerRuns pins the fact the session
// network anchor exists for. A network's gateway address is reported by
// `container network inspect` from the moment the network is created, but it
// is not on a host interface until a container on that network *starts*, and
// it leaves again when the last running container stops. Nothing on the host
// can bind it outside that window.
//
// If this test ever fails, the anchor may no longer be necessary - but read
// the spec before deleting anything, because Up, Down and attach all assume
// the address behaves this way.
func TestGatewayBindableOnlyWhileAContainerRuns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const netName = "saddle-gwcontract"
	_ = DeleteNetwork(ctx, netName) // a previous crashed run
	n, err := CreateNetwork(ctx, netName, false)
	if err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}
	defer func() { _ = DeleteNetwork(context.WithoutCancel(ctx), netName) }()

	if bindable(n.Gateway) {
		t.Fatalf("gateway %s was bindable straight after network create", n.Gateway)
	}

	h, err := Create(ctx, Spec{
		Name:    "saddle-gwcontract-c",
		Image:   img,
		Network: netName,
		Cmd:     []string{"sleep", "300"},
		CPUs:    1,
		Memory:  "256m",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = Remove(context.WithoutCancel(ctx), h) }()

	if bindable(n.Gateway) {
		t.Fatalf("gateway %s was bindable after container create, before start", n.Gateway)
	}

	if err := Start(ctx, h.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := waitBindableForTest(n.Gateway, 30*time.Second, true); err != nil {
		t.Fatalf("after start: %v", err)
	}

	if _, err := run(ctx, "stop", h.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := waitBindableForTest(n.Gateway, 30*time.Second, false); err != nil {
		t.Fatalf("after stop: %v", err)
	}
}

// waitBindableForTest polls until bindable(host) == want, so the assertions
// above tolerate the lag between the CLI returning and the host's interfaces
// settling, without asserting a specific lag.
func waitBindableForTest(host string, timeout time.Duration, want bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if bindable(host) == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("gateway %s bindable != %v after %s", host, want, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func fileUID(t *testing.T, fi os.FileInfo) int {
	t.Helper()
	// syscall.Stat_t exposes Uid as a field, not a method, so an interface
	// assertion cannot reach it.
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("cannot determine uid: unexpected FileInfo.Sys type %T", fi.Sys())
	}
	return int(st.Uid)
}
