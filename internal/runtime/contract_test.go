//go:build contract

package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const img = "docker.io/library/alpine:3.20"

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
		Cmd: []string{"sh", "-c",
			"wget -q -T 4 -O- http://example.com >/dev/null 2>&1 && echo INTERNET || echo NOINTERNET"},
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
		t.Fatalf("network contract violated: container reached the internet\n%s", out)
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
