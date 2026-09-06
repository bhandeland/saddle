package runtime

import (
	"strings"
	"testing"
)

func TestParseGatewayReadsStatusField(t *testing.T) {
	// Real output shape from `container network inspect`, verified 2026-09-05.
	out := `[
  {
    "configuration" : { "mode" : "hostOnly", "name" : "saddle-x" },
    "id" : "saddle-x",
    "status" : {
      "ipv4Gateway" : "192.168.128.1",
      "ipv4Subnet" : "192.168.128.0\/24"
    }
  }
]`
	gw, err := parseGateway([]byte(out))
	if err != nil {
		t.Fatalf("parseGateway: %v", err)
	}
	if gw != "192.168.128.1" {
		t.Fatalf("got %q want 192.168.128.1", gw)
	}
}

func TestParseGatewayErrorsOnEmptyList(t *testing.T) {
	if _, err := parseGateway([]byte(`[]`)); err == nil {
		t.Fatal("expected error for empty inspect output")
	}
}

func TestCreateArgsCarryMountsEnvAndResources(t *testing.T) {
	args := createArgs(Spec{
		Name:    "saddle-demo",
		Image:   "alpine:3.20",
		Network: "saddle-demo-net",
		Workdir: "/work",
		Cmd:     []string{"claude", "--dangerously-skip-permissions"},
		Env:     map[string]string{"HTTPS_PROXY": "http://192.168.128.1:9000"},
		Mounts:  []Mount{{Source: "/host/wt", Target: "/work"}},
		CPUs:    4,
		Memory:  "8g",
	})
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"create", "--name saddle-demo", "--network saddle-demo-net",
		"--volume /host/wt:/work", "--workdir /work",
		"--env HTTPS_PROXY=http://192.168.128.1:9000",
		"--cpus 4", "--memory 8g", "alpine:3.20",
		"claude --dangerously-skip-permissions",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("createArgs missing %q\ngot: %s", want, joined)
		}
	}
}

func TestParseRunningReadsContainerIDs(t *testing.T) {
	// Real `container list` header, verified 2026-09-05.
	out := `ID  IMAGE  OS  ARCH  STATE  IP  CPUS  MEMORY  STARTED
saddle-a  alpine  linux  arm64  running  192.168.64.2  4  8g  now
saddle-b  alpine  linux  arm64  running  192.168.64.3  4  8g  now`
	got := parseRunning(out)
	if !got["saddle-a"] || !got["saddle-b"] {
		t.Fatalf("missing containers: %v", got)
	}
	if got["ID"] {
		t.Fatal("header row parsed as a container")
	}
}

func TestParseRunningEmptyListing(t *testing.T) {
	out := "ID  IMAGE  OS  ARCH  STATE  IP  CPUS  MEMORY  STARTED"
	if got := parseRunning(out); len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

func TestParseStatusMatchesRunningRow(t *testing.T) {
	out := `ID  IMAGE  OS  ARCH  STATE  IP  CPUS  MEMORY  STARTED
saddle-a  alpine  linux  arm64  running  192.168.64.2  4  8g  now`
	if got := parseStatus(out, "saddle-a"); got != "running" {
		t.Fatalf("got %q want running", got)
	}
}

func TestParseStatusMatchesStoppedRow(t *testing.T) {
	out := `ID  IMAGE  OS  ARCH  STATE  IP  CPUS  MEMORY  STARTED
saddle-a  alpine  linux  arm64  stopped  192.168.64.2  4  8g  now`
	if got := parseStatus(out, "saddle-a"); got != "stopped" {
		t.Fatalf("got %q want stopped", got)
	}
}

func TestParseStatusNoMatchReturnsEmpty(t *testing.T) {
	out := `ID  IMAGE  OS  ARCH  STATE  IP  CPUS  MEMORY  STARTED
saddle-a  alpine  linux  arm64  running  192.168.64.2  4  8g  now`
	if got := parseStatus(out, "saddle-nonexistent"); got != "" {
		t.Fatalf("got %q want empty", got)
	}
}

func TestParseStatusHeaderOnlyReturnsEmpty(t *testing.T) {
	out := "ID  IMAGE  OS  ARCH  STATE  IP  CPUS  MEMORY  STARTED"
	if got := parseStatus(out, "saddle-a"); got != "" {
		t.Fatalf("got %q want empty", got)
	}
}

func TestCreateArgsMarksReadOnlyMounts(t *testing.T) {
	args := createArgs(Spec{
		Name:  "x", Image: "alpine",
		Mounts: []Mount{{Source: "/skills", Target: "/skills", ReadOnly: true}},
	})
	if !strings.Contains(strings.Join(args, " "), "/skills:/skills:ro") {
		t.Fatalf("read-only mount not marked: %v", args)
	}
}
