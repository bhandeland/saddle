# Session Network Anchor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every saddle session an anchor container that keeps its network's gateway address plumbed onto the host, so the egress proxy and spawned host services can bind it - and so `saddle attach` can rebind them after the original `up` exits.

**Architecture:** Apple `container` materialises a network's gateway address on the host only while a container on that network is *running*, and removes it when the last one stops. A per-session anchor container (`<session>-anchor`, the profile's own image, `sleep infinity`, 1 cpu / 64m) holds that address open for the session's lifetime. `Up` starts it before binding anything; `Down` removes it after the session container and before the network; `attach` ensures it is running before rebinding the proxy at the exact recorded address.

**Tech Stack:** Go 1.24, Apple `container` 1.3.1 CLI, no new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-06-session-network-anchor-design.md`

## Global Constraints

- macOS 26+ and Apple `container` 1.3.1+. Nothing here works on Docker.
- The anchor runs the **profile's own image**, never a hardcoded one. That image must contain `sleep`.
- Anchor resources are exactly 1 cpu and `"64m"`.
- An anchor is created for **every** session, including `--open-net`, because spawned host services bind the gateway even when saddle starts no proxy.
- Teardown failures on the anchor **warn on stderr and continue**. They must never abort the rest of a teardown - a partial teardown produces a session removable only with `--force`, which also bypasses the uncommitted-changes guard.
- No new Go dependencies. The quality gate allows only gofumpt, golangci-lint and gotestsum.
- `make check` must pass before every commit. It runs fmt, then lint, then tests, and stops at the first failure.
- Contract tests are build-tagged and are **not** run by `make check`. Run them explicitly: `go test -tags contract ./internal/runtime/`.

---

### Task 1: `hostsvc.WaitBindable`

Polling for "can this process bind that address yet" is its own small unit. It goes in `hostsvc` beside `WaitReady`, which polls the mirror-image question ("is something listening there yet").

**Files:**
- Modify: `internal/hostsvc/hostsvc.go`
- Test: `internal/hostsvc/hostsvc_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func WaitBindable(host string, timeout time.Duration) error` - takes a bare host or IP (no port), returns nil once a TCP listener can be opened on it, or an error naming the address after timeout.

- [ ] **Step 1: Write the failing tests**

Append to `internal/hostsvc/hostsvc_test.go`:

```go
func TestWaitBindableReturnsImmediatelyForAnAddressWeHave(t *testing.T) {
	if err := WaitBindable("127.0.0.1", 2*time.Second); err != nil {
		t.Fatalf("WaitBindable(127.0.0.1): %v", err)
	}
}

// 192.0.2.0/24 is TEST-NET-1: reserved for documentation and guaranteed not
// to be assigned to an interface, so this is a deterministic negative rather
// than a hope about the machine's configuration.
func TestWaitBindableFailsForAnAddressWeDoNotHave(t *testing.T) {
	start := time.Now()
	err := WaitBindable("192.0.2.1", 300*time.Millisecond)
	if err == nil {
		t.Fatal("WaitBindable succeeded on an unassigned address")
	}
	if !strings.Contains(err.Error(), "192.0.2.1") {
		t.Fatalf("error does not name the address: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("WaitBindable overran its timeout: %s", elapsed)
	}
}
```

Add `"strings"` to that file's imports if it is not already there.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/hostsvc/ -run WaitBindable`
Expected: FAIL, `undefined: WaitBindable`.

- [ ] **Step 3: Implement it**

Add to `internal/hostsvc/hostsvc.go`, directly below `WaitReady`:

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/hostsvc/ -run WaitBindable -v`
Expected: both PASS.

- [ ] **Step 5: Run the gate and commit**

```bash
make check
git add internal/hostsvc/hostsvc.go internal/hostsvc/hostsvc_test.go
git commit -m "Add hostsvc.WaitBindable for an address that does not exist yet"
```

---

### Task 2: `runtime.Start`, and a contract test that pins the gateway lifecycle

The contract test is the keystone of the whole design: it asserts the measured behaviour the anchor exists to work around. Write it here, before anything depends on it, so a future container release that changes this fails loudly rather than silently.

**Files:**
- Modify: `internal/runtime/applecontainer.go`
- Test: `internal/runtime/contract_test.go`

**Interfaces:**
- Consumes: `runtime.CreateNetwork`, `runtime.DeleteNetwork`, `runtime.Create`, `runtime.Remove`, `runtime.Spec`, `runtime.Handle` (all existing).
- Produces: `func Start(ctx context.Context, id string) error` - starts an existing container detached and returns, unlike `AttachArgv`'s `container start -ai`, which starts *and* attaches.

- [ ] **Step 1: Write the failing contract test**

Append to `internal/runtime/contract_test.go`:

```go
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
		Memory:  "64m",
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
```

Add `"fmt"` to that file's imports if it is not already there.

- [ ] **Step 2: Run the contract test to verify it fails**

Run: `go test -tags contract ./internal/runtime/ -run TestGatewayBindableOnlyWhileAContainerRuns`
Expected: FAIL, `undefined: Start`.

- [ ] **Step 3: Implement `Start`**

Add to `internal/runtime/applecontainer.go`, directly below `AttachArgv`:

```go
// Start starts an existing container without attaching to it.
//
// AttachArgv's `container start -ai` both starts and attaches, which is what
// an operator wants and what the session container uses. The anchor needs the
// start without the attach: nothing ever looks at its output.
func Start(ctx context.Context, id string) error {
	_, err := run(ctx, "start", id)
	return err
}
```

- [ ] **Step 4: Run the contract test to verify it passes**

Run: `go test -tags contract ./internal/runtime/ -run TestGatewayBindableOnlyWhileAContainerRuns -v`
Expected: PASS. It takes roughly a minute; it starts and stops a real container.

If it fails at the *first* assertion (bindable straight after network create), stop and re-read the spec: the platform behaviour has changed and the design's premise is gone.

- [ ] **Step 5: Run the gate and commit**

```bash
make check
go test -tags contract ./internal/runtime/
git add internal/runtime/applecontainer.go internal/runtime/contract_test.go
git commit -m "Pin the gateway's bind lifecycle in a contract test, and add runtime.Start"
```

---

### Task 3: Anchor naming and spec

Pure functions, so they get ordinary unit tests. Keeping the spec construction separate from `Up` is what makes "the anchor carries no mounts, no env and no token" an assertion rather than a claim.

**Files:**
- Create: `internal/session/anchor.go`
- Test: `internal/session/anchor_test.go`

**Interfaces:**
- Consumes: `runtime.Spec` (existing).
- Produces:
  - `func AnchorName(session string) string`
  - `func AnchorSpec(session, image, network string) runtime.Spec`

- [ ] **Step 1: Write the failing tests**

Create `internal/session/anchor_test.go`:

```go
package session

import "testing"

func TestAnchorNameDerivesFromTheSession(t *testing.T) {
	if got := AnchorName("saddle-main"); got != "saddle-main-anchor" {
		t.Fatalf("AnchorName = %q", got)
	}
}

func TestAnchorSpecRunsNothingAndCarriesNothing(t *testing.T) {
	s := AnchorSpec("saddle-main", "saddle-verify:local", "saddle-main-net")

	if s.Name != "saddle-main-anchor" {
		t.Errorf("Name = %q", s.Name)
	}
	if s.Image != "saddle-verify:local" {
		t.Errorf("Image = %q", s.Image)
	}
	if s.Network != "saddle-main-net" {
		t.Errorf("Network = %q", s.Network)
	}
	// The anchor holds an address open. Anything it could read or write is a
	// liability, not a feature: no worktree, no mcp.json, no skills, and
	// above all no CLAUDE_CODE_OAUTH_TOKEN.
	if len(s.Mounts) != 0 {
		t.Errorf("Mounts = %v, want none", s.Mounts)
	}
	if len(s.Env) != 0 {
		t.Errorf("Env = %v, want none", s.Env)
	}
	if s.Workdir != "" {
		t.Errorf("Workdir = %q, want empty", s.Workdir)
	}
}

func TestAnchorSpecIsSmallAndLongLived(t *testing.T) {
	s := AnchorSpec("x", "img", "net")
	if s.CPUs != 1 {
		t.Errorf("CPUs = %d, want 1", s.CPUs)
	}
	if s.Memory != "64m" {
		t.Errorf("Memory = %q, want 64m", s.Memory)
	}
	want := []string{"sleep", "infinity"}
	if len(s.Cmd) != len(want) || s.Cmd[0] != want[0] || s.Cmd[1] != want[1] {
		t.Errorf("Cmd = %v, want %v", s.Cmd, want)
	}
}

// The anchor must use the profile's image, never a hardcoded one: it is the
// image already pulled for the session, so it costs no second pull and there
// is no second thing to keep current.
func TestAnchorSpecUsesWhateverImageItIsGiven(t *testing.T) {
	if got := AnchorSpec("x", "ghcr.io/example/other:9", "net").Image; got != "ghcr.io/example/other:9" {
		t.Fatalf("Image = %q", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/session/ -run Anchor`
Expected: FAIL, `undefined: AnchorName`.

- [ ] **Step 3: Implement**

Create `internal/session/anchor.go`:

```go
package session

import "github.com/brandon/saddle/internal/runtime"

// The anchor is deliberately tiny and deliberately idle. It is not a place
// anything runs.
const (
	anchorCPUs   = 1
	anchorMemory = "64m"
)

// AnchorName names the anchor container belonging to a session.
func AnchorName(session string) string { return session + "-anchor" }

// AnchorSpec builds the anchor container for a session.
//
// Apple container puts a network's gateway address on the host only while a
// container on that network is running, and takes it away again when the last
// one stops. Every host-side listener a session needs - the egress proxy, and
// any carry_in.mcp spawn - binds that address, so something on the network has
// to be running before any of them can start, and has to stay running for as
// long as the session might come back. That is the anchor's whole job: it is a
// refcount held open, in the shape of a container.
//
// It runs the profile's own image because that image is already local (the
// session needs it regardless), which costs no second pull and leaves nothing
// extra to keep current. The one requirement this puts on a profile's image is
// that it has `sleep`.
func AnchorSpec(session, image, network string) runtime.Spec {
	return runtime.Spec{
		Name:    AnchorName(session),
		Image:   image,
		Network: network,
		Cmd:     []string{"sleep", "infinity"},
		CPUs:    anchorCPUs,
		Memory:  anchorMemory,
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/session/ -run Anchor -v`
Expected: all four PASS.

- [ ] **Step 5: Run the gate and commit**

```bash
make check
git add internal/session/anchor.go internal/session/anchor_test.go
git commit -m "Add the session anchor container's name and spec"
```

---

### Task 4: Record the anchor, the allowlist, and each spawned service's address in state

`attach` has to rebuild the host side from the state file alone. Three things it needs are not written down yet.

`Allow` is the sharp one: `egressSummary` is lossy on purpose - it renders `["a","b","c"]` as `"a +2"` for the `ls` column - so the allowlist genuinely cannot be recovered from `Egress`, and an attach that guessed would silently widen or narrow containment.

**Files:**
- Modify: `internal/session/state.go`
- Test: `internal/session/state_test.go`

**Interfaces:**
- Consumes: `State`, `Spawned` (existing).
- Produces: `State.Anchor string`, `State.Allow []string`, `Spawned.Addr string`.

- [ ] **Step 1: Write the failing test**

Append to `internal/session/state_test.go`:

```go
func TestStateRoundTripsAnchorAllowlistAndSpawnedAddr(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	want := State{
		Name:      "acme-main",
		Anchor:    "acme-main-anchor",
		Network:   "acme-main-net",
		Container: "acme-main",
		ProxyAddr: "192.168.65.1:54321",
		Allow:     []string{"api.anthropic.com", "proxy.golang.org"},
		Spawned: []Spawned{{
			Name: "remem",
			PID:  4242,
			Cmd:  []string{"remem", "serve", "--http"},
			Addr: "192.168.65.1:9100",
		}},
	}
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := Load("acme-main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Anchor != want.Anchor {
		t.Errorf("Anchor = %q, want %q", got.Anchor, want.Anchor)
	}
	if len(got.Allow) != 2 || got.Allow[0] != "api.anthropic.com" || got.Allow[1] != "proxy.golang.org" {
		t.Errorf("Allow = %v, want %v", got.Allow, want.Allow)
	}
	if len(got.Spawned) != 1 || got.Spawned[0].Addr != "192.168.65.1:9100" {
		t.Errorf("Spawned = %+v", got.Spawned)
	}
}

// State files written before the anchor existed must still load: an operator
// mid-upgrade has sessions on disk, and a parse failure would strand them
// with no way to run `saddle down`.
func TestStateWithoutAnchorStillLoads(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := Save(State{Name: "old-session", Container: "old-session"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load("old-session")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Anchor != "" {
		t.Fatalf("Anchor = %q, want empty", got.Anchor)
	}
}
```

`Dir()` honours `XDG_STATE_HOME` ahead of the home directory, which is how every other test in `state_test.go` isolates itself. Match that.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/session/ -run 'StateRoundTripsAnchor|StateWithoutAnchor'`
Expected: FAIL, `unknown field Anchor in struct literal`.

- [ ] **Step 3: Add the fields**

In `internal/session/state.go`, add to `Spawned`:

```go
	// Addr is where this server was told to listen, so a later attach can
	// wait for it to come back without re-deriving it from the profile.
	Addr string `json:"addr,omitempty"`
```

And to `State`, after `Container`:

```go
	// Anchor is the container that keeps the session network's gateway
	// address on the host. Empty in state files written before anchors
	// existed; teardown treats that as "nothing to remove".
	Anchor string `json:"anchor,omitempty"`
```

And after `Egress`:

```go
	// Allow is the effective egress allowlist, recorded because Egress is a
	// lossy display summary ("api.anthropic.com +2") and attach must rebind
	// the proxy with exactly the containment the session was created with.
	Allow []string `json:"allow,omitempty"`
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/session/ -run 'StateRoundTripsAnchor|StateWithoutAnchor' -v`
Expected: both PASS.

- [ ] **Step 5: Run the gate and commit**

```bash
make check
git add internal/session/state.go internal/session/state_test.go
git commit -m "Record the anchor, the allowlist and each spawned address in session state"
```

---

### Task 5: Start the anchor in `Up`, before anything binds

This is the task that makes `saddle up` work at all. Verify it by hand at the end - it is the first point in the plan where a session can actually start.

**Files:**
- Modify: `internal/session/up.go` (unwind block around lines 205-247; step 4 around lines 303-310; step 5 around lines 311-327; the `Save` call around line 402)

**Interfaces:**
- Consumes: `AnchorName`, `AnchorSpec` (Task 3), `runtime.Start` (Task 2), `hostsvc.WaitBindable` (Task 1), `State.Anchor` and `State.Allow` (Task 4).
- Produces: a running anchor per session, recorded in `State.Anchor`.

- [ ] **Step 1: Add the timeout constant**

In `internal/session/up.go`, beside the existing `SpawnReadyTimeout`:

```go
// GatewayBindTimeout bounds the wait for a freshly started anchor to put the
// session network's gateway address onto the host. Generous on purpose: the
// alternative to waiting is a bind error the operator cannot act on.
const GatewayBindTimeout = 30 * time.Second
```

- [ ] **Step 2: Declare the unwind variables**

In the `var (...)` block inside `Up` (around line 206), after `netName`:

```go
		anchorCreated  bool
		anchorName     string
```

- [ ] **Step 3: Unwind the anchor**

In the deferred cleanup, between the spawned/proxy teardown and `DeleteNetwork`:

```go
		// After the proxy and spawned services, which are bound to the
		// gateway this anchor is holding up, and before DeleteNetwork, which
		// cannot run while a container is still on the network. A failure
		// here is reported and stepped over: aborting teardown now would
		// leave a half-removed session that only --force can clear, and
		// --force also skips the uncommitted-changes guard.
		if anchorCreated {
			if err := runtime.Remove(cleanupCtx, runtime.Handle{ID: anchorName}); err != nil {
				fmt.Fprintf(os.Stderr, "saddle: could not remove anchor %s: %v\n", anchorName, err)
			}
		}
```

- [ ] **Step 4: Start the anchor between steps 4 and 5**

Immediately after `netCreated = true` and before the `// 5. Egress proxy` comment:

```go
	// 4b. Anchor. Apple container puts the gateway address on the host only
	// while a container on the network is running, so nothing below can bind
	// it until something is up. See
	// docs/superpowers/specs/2026-09-06-session-network-anchor-design.md.
	anchorName = AnchorName(name)
	if _, err := runtime.Create(ctx, AnchorSpec(name, p.Image, netName)); err != nil {
		return State{}, fmt.Errorf("create anchor: %w", err)
	}
	anchorCreated = true
	if err := runtime.Start(ctx, anchorName); err != nil {
		return State{}, fmt.Errorf("start anchor: %w", err)
	}

	// 4c. The address appears a beat after the start returns, so wait for it
	// rather than assuming it.
	if err := hostsvc.WaitBindable(n.Gateway, GatewayBindTimeout); err != nil {
		return State{}, fmt.Errorf("session gateway never came up: %w", err)
	}
```

- [ ] **Step 5: Record the anchor and allowlist in the state**

In the `st := State{...}` literal, add `Anchor: anchorName,` next to `Container: h.ID,` and `Allow: allow,` next to `Egress: egressSummary(...)`.

- [ ] **Step 6: Record each spawned service's address**

In `startSpawned`, change the recording line to carry the address it already computed:

```go
		rec = append(rec, Spawned{Name: name, PID: pr.PID, Cmd: pr.Cmd, Line: pr.Line, Addr: addr})
```

- [ ] **Step 7: Run the gate**

Run: `make check`
Expected: 0 issues, all packages pass. No new unit test here - the behaviour is ordering against a real CLI, which Task 2's contract test pins and the manual check below proves.

- [ ] **Step 8: Verify by hand that a session now starts**

```bash
go build -o saddle ./cmd/saddle
./saddle up -render cmux -name anchor1 .
```

Expected: no `bind egress proxy` error; the summary prints; a cmux workspace opens. Then, from another shell:

```bash
container list          # expect anchor1 AND anchor1-anchor, both running
./saddle ls             # expect exactly one session, anchor1 - never the anchor
```

- [ ] **Step 9: Verify the unwind, then tear down**

Point a profile's `carry_in.mcp.<name>.spawn` at a binary that does not exist, run `saddle up` again, and confirm it fails *and* leaves nothing behind:

```bash
container list                 # no anchor, no session container
container network list         # no session network
```

Then remove the good session: `./saddle down anchor1` (it will not clean up the anchor until Task 6 - remove it by hand with `container rm -f anchor1-anchor` for now).

- [ ] **Step 10: Commit**

```bash
git add internal/session/up.go
git commit -m "Anchor the session network before binding anything to its gateway"
```

---

### Task 6: Remove the anchor in `Down`

**Files:**
- Modify: `internal/session/up.go` (`Down`, around lines 535-598)

**Interfaces:**
- Consumes: `State.Anchor` (Task 4).
- Produces: nothing new.

- [ ] **Step 1: Remove the anchor between the spawned reap and `DeleteNetwork`**

In `Down`, after the `for _, s := range st.Spawned` loop and before `_ = runtime.DeleteNetwork(ctx, st.Network)`:

```go
	// After the spawned reap, which identifies processes bound to the
	// gateway this anchor holds up, and before DeleteNetwork, which cannot
	// remove a network that still has a container on it. Empty on state
	// files written before anchors existed.
	//
	// Reported and stepped over, never fatal: this runs after the container
	// is gone, so returning here would leave a session that only --force can
	// remove - and --force is the flag that also skips the dirty-worktree
	// guard, so making it the only way out is exactly backwards.
	if st.Anchor != "" {
		if err := runtime.Remove(ctx, runtime.Handle{ID: st.Anchor}); err != nil {
			fmt.Fprintf(os.Stderr, "saddle: could not remove anchor %s: %v\n", st.Anchor, err)
		}
	}
```

- [ ] **Step 2: Run the gate**

Run: `make check`
Expected: 0 issues, all packages pass.

- [ ] **Step 3: Verify a full round trip by hand**

```bash
go build -o saddle ./cmd/saddle
./saddle up -render cmux -name anchor2 .
container list                     # anchor2 and anchor2-anchor
./saddle down anchor2
container list                     # neither remains
container network list             # no anchor2-net
./saddle ls                        # no stale entry
```

- [ ] **Step 4: Verify teardown of a session whose anchor is already gone**

```bash
./saddle up -render cmux -name anchor3 .
container rm -f anchor3-anchor     # simulate a hand-killed anchor
./saddle down anchor3
```

Expected: a `could not remove anchor` warning on stderr, and teardown still completes - no container, no network, no state entry, worktree removed.

- [ ] **Step 5: Commit**

```bash
git add internal/session/up.go
git commit -m "Remove the session anchor in Down, after the container and before the network"
```

---

### Task 7: Rebuild the host side in `saddle attach`

Today `attach` re-runs `container start -ai` and nothing else, so a session re-entered after the original `up` exited has no proxy and no memory server: egress fails closed and looks like the network is down, and MCP calls error. With the anchor holding the address, both can be rebound.

The logic belongs in `session`, not in `main.go`, so `main.go` stays a flag parser.

**Files:**
- Create: `internal/session/attach.go`
- Modify: `cmd/saddle/main.go` (`cmdAttach`, around lines 200-220)
- Test: `internal/session/attach_test.go`

**Interfaces:**
- Consumes: `Load`, `State.Anchor`, `State.ProxyAddr`, `State.Allow`, `State.Spawned` (Tasks 4-6), `runtime.Start`, `hostsvc.WaitBindable`, `hostsvc.Start`, `hostsvc.WaitReady`, `egress.New`, `render.Attach`.
- Produces:
  - `func Attach(ctx context.Context, name string, mode render.Mode) error`
  - `func proxyPortFree(addr string) error` - nil if nothing holds addr.

- [ ] **Step 1: Write the failing test for the conflict check**

`Attach` itself drives real containers, so the unit test covers the one decision that is pure: refusing when the recorded proxy address is already held.

Create `internal/session/attach_test.go`:

```go
package session

import (
	"net"
	"strings"
	"testing"
)

func TestProxyPortFreeAcceptsAnUnusedAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // now nothing holds it

	if err := proxyPortFree(addr); err != nil {
		t.Fatalf("proxyPortFree(%s) = %v, want nil", addr, err)
	}
}

// Two saddle processes attached to one session would share a proxy without
// either knowing. Refusing beats silently sharing containment.
func TestProxyPortFreeRefusesAnAddressSomethingElseHolds(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	err = proxyPortFree(addr)
	if err == nil {
		t.Fatal("proxyPortFree accepted an address already in use")
	}
	if !strings.Contains(err.Error(), addr) {
		t.Fatalf("error does not name the address: %v", err)
	}
	if !strings.Contains(err.Error(), "already attached") {
		t.Fatalf("error does not explain the likely cause: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/session/ -run ProxyPortFree`
Expected: FAIL, `undefined: proxyPortFree`.

- [ ] **Step 3: Implement `Attach`**

Create `internal/session/attach.go`:

```go
package session

import (
	"context"
	"fmt"
	"net"

	"github.com/brandon/saddle/internal/egress"
	"github.com/brandon/saddle/internal/hostsvc"
	"github.com/brandon/saddle/internal/render"
	"github.com/brandon/saddle/internal/runtime"
)

// proxyPortFree reports whether addr can be bound, so attach can refuse
// rather than race another saddle process for the same session.
func proxyPortFree(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("egress proxy address %s is already in use; another saddle process is probably already attached to this session", addr)
	}
	return ln.Close()
}

// Attach re-enters an existing session.
//
// The host side of a session belongs to whichever saddle process is attached:
// `up` binds the egress proxy and starts the carried-in servers, and kills
// them when it exits. So a session re-entered later has no proxy and no
// memory server, and the symptoms are misleading - egress fails closed and
// looks like the network is down, MCP calls error against a dead endpoint.
//
// Rebuilding that host side is only possible because the anchor kept the
// gateway address on the host after the session container stopped. The proxy
// must come back on *exactly* the recorded address: the container's
// HTTPS_PROXY was baked in when it was created and still points there.
func Attach(ctx context.Context, name string, mode render.Mode) error {
	st, err := Load(name)
	if err != nil {
		return err
	}

	// 1. The anchor, so the gateway address exists before anything binds it.
	if st.Anchor != "" {
		if err := runtime.Start(ctx, st.Anchor); err != nil {
			return fmt.Errorf("start anchor %s: %w (was it removed? tear the session down with `saddle down %s`)", st.Anchor, err, name)
		}
	}

	// 2. The egress proxy, at the address the container already believes in.
	var px *egress.Proxy
	if st.ProxyAddr != "" {
		host, _, splitErr := net.SplitHostPort(st.ProxyAddr)
		if splitErr != nil {
			return fmt.Errorf("state file records an unusable proxy address %q: %w", st.ProxyAddr, splitErr)
		}
		if err := hostsvc.WaitBindable(host, GatewayBindTimeout); err != nil {
			return fmt.Errorf("session gateway never came up: %w", err)
		}
		if err := proxyPortFree(st.ProxyAddr); err != nil {
			return err
		}
		px = egress.New(st.Allow)
		if _, err := px.Listen(st.ProxyAddr); err != nil {
			return fmt.Errorf("rebind egress proxy on %s: %w", st.ProxyAddr, err)
		}
		defer func() { _ = px.Close() }()
	}

	// 3. The carried-in servers, from the argv recorded when they last ran.
	var procs []*hostsvc.Proc
	defer func() {
		for _, pr := range procs {
			_ = pr.Kill()
		}
	}()
	for _, s := range st.Spawned {
		if len(s.Cmd) == 0 {
			continue
		}
		pr, err := hostsvc.Start(s.Cmd)
		if err != nil {
			return fmt.Errorf("restart %s: %w", s.Name, err)
		}
		procs = append(procs, pr)
		if s.Addr == "" {
			continue // recorded before addresses were, so nothing to wait on
		}
		if err := hostsvc.WaitReady(s.Addr, SpawnReadyTimeout); err != nil {
			return fmt.Errorf("restart %s: %w", s.Name, err)
		}
	}

	return render.Attach(ctx, mode, st.Name, st.Worktree, runtime.AttachArgv(st.Container))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/session/ -run ProxyPortFree -v`
Expected: both PASS.

- [ ] **Step 5: Point `cmdAttach` at it**

Replace the body of `cmdAttach` in `cmd/saddle/main.go` after the flag parsing and `fs.NArg()` check with:

```go
	mode := render.Mode(*renderer)
	if mode == "" {
		mode = render.Auto()
	}
	return session.Attach(ctx, fs.Arg(0), mode)
```

The `session.Load` call and the `st` variable go away with it. Leave
`cmd/saddle/main.go`'s `runtime` import alone - `cmdLs` still uses
`runtime.Running`.

- [ ] **Step 6: Run the gate**

Run: `make check`
Expected: 0 issues, all packages pass.

- [ ] **Step 7: Verify attach-after-exit by hand**

This is the gotcha the design set out to close, so prove it rather than assume it.

```bash
go build -o saddle ./cmd/saddle
./saddle up -render cmux -name anchor4 .
```

Exit the claude session inside the cmux workspace, and let the original `saddle up` return. Then:

```bash
container list                              # anchor4-anchor still running; anchor4 stopped
ps ax | grep 'remem serve'                  # nothing - the up process killed it
./saddle attach anchor4
```

Expected: attach succeeds; from inside the session, egress to an allowlisted host works and the carried-in `recall` tool returns results. Then, while that attach is live, run `./saddle attach anchor4` again from another shell and expect a refusal naming the address, not a second silent proxy.

Tear down: `./saddle down anchor4`.

- [ ] **Step 8: Commit**

```bash
git add internal/session/attach.go internal/session/attach_test.go cmd/saddle/main.go
git commit -m "Rebuild the host side of a session on attach"
```

---

### Task 8: Document the anchor, and extend the manual checklist

**Files:**
- Modify: `README.md` (the "Create a profile" section)
- Modify: `docs/MANUAL-VERIFICATION.md`
- Modify: `docs/superpowers/specs/2026-09-06-session-network-anchor-design.md` (status line only)

- [ ] **Step 1: Document the image requirement and the anchor in the README**

In the "Create a profile" section, after the paragraph describing `carry_in.mcp.<name>.spawn`, add:

```markdown
Every session also runs a second, idle container called `<session>-anchor`,
using the same image, with 1 cpu and 64m. It exists because Apple `container`
puts a network's gateway address on the host only while a container on that
network is running - and the egress proxy and any carried-in server bind
exactly that address. The anchor holds it open, which is also what lets
`saddle attach` bring a session's egress and memory server back after the
original `saddle up` has exited. `saddle down` removes it; it never appears
in `saddle ls`.

The anchor puts one requirement on a profile's image: it must have `sleep`.
Any image with `claude`, `git` and a shell does; a distroless one would not.
```

- [ ] **Step 2: Add the anchor items to the manual checklist**

In `docs/MANUAL-VERIFICATION.md`, add a section before "## Carried-in remem over HTTP":

```markdown
## Session network anchor

- [ ] `saddle up`: confirm `container list` shows both `<name>` and
      `<name>-anchor` running, and that `saddle ls` shows only the session.
- [ ] `saddle down`: confirm both containers and the network are gone.
- [ ] Remove the anchor by hand (`container rm -f <name>-anchor`), then
      `saddle down`: confirm it warns and still tears everything else down,
      without needing `--force`.
- [ ] Exit the session so the original `saddle up` returns, then
      `saddle attach <name>`: confirm egress to an allowlisted host works
      again and the carried-in `recall` returns results. This is the case
      that was broken before the anchor: the gateway address left the host
      when the container stopped, so nothing could rebind it.
- [ ] With one attach live, run `saddle attach <name>` again elsewhere:
      confirm it refuses and names the proxy address rather than starting a
      second proxy.
```

- [ ] **Step 3: Mark the spec implemented**

Change the spec's status line from `**Status:** approved, not implemented` to `**Status:** implemented`.

- [ ] **Step 4: Run the gate and the contract tests**

```bash
make check
go test -tags contract ./internal/runtime/
```

- [ ] **Step 5: Commit**

```bash
git add README.md docs/MANUAL-VERIFICATION.md docs/superpowers/specs/2026-09-06-session-network-anchor-design.md
git commit -m "Document the session anchor and add it to the manual checklist"
```
