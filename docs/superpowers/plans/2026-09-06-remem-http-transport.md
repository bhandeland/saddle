# remem HTTP Transport and saddle Host Services Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a saddle-contained Claude Code session use remem, by giving remem a streamable-HTTP transport and giving saddle a generic way to start a host-side MCP server on the session gateway.

**Architecture:** remem gains `remem serve --http --host H --port P --project NAME`, which runs the existing seven-tool `MCPServer` over streamable-HTTP with the project pinned at launch instead of derived from the process's working directory. saddle gains `carry_in.mcp.<name>.spawn`, a command template it expands, starts after the session network exists, waits for, and reaps at teardown. saddle learns nothing about remem.

**Tech Stack:** Python 3, typer, `mcp` SDK 2.1.1 (`mcp.server.mcpserver.MCPServer`), pytest. Go 1.24, `gopkg.in/yaml.v3`, standard library only.

**Spec:** `docs/superpowers/specs/2026-09-06-remem-http-transport-design.md` (in the saddle repo)

## Global Constraints

- **Two repositories.** Tasks 1-3 are in `/Users/brandon/llmworkspace/remem`. Tasks 4-7 are in `/Users/brandon/llmworkspace/saddle`. Every task names its repo. Commit in the repo the task touches; never stage across both.
- **saddle has exactly one dependency project-wide:** `gopkg.in/yaml.v3`. Do not add another. Everything in tasks 4-7 uses the Go standard library.
- **remem's stdio behaviour must not change, byte for byte.** Bare `remem serve` keeps working exactly as it does today. Every task that touches `mcp_server.py` or `cli.py` proves this with a test.
- **A field that parses and does nothing is refused, not ignored.** `internal/profile/profile.go` already rejects `carry_in.ssh_agent` and `carry_in.mcp.<name>.tools` on that principle. `spawn` ships fully wired: arguments really expanded, process really started, readiness really awaited, child really killed.
- **remem test commands:** `uv run pytest` (full), `uv run pytest -m 'not db'` (no Postgres). Tests in this plan need no Postgres and carry **no** `db` marker.
- **saddle test command:** `go test ./...`
- **`transport_security` is load-bearing.** `TransportSecuritySettings.enable_dns_rebinding_protection` defaults to `True`, so binding to a gateway address without allowlisting `host:port` rejects every request.
- **Default port is 9100.** Each saddle session has its own gateway address, so `gw1:9100` and `gw2:9100` are different sockets. Do not build ephemeral-port negotiation.

---

## File Structure

**remem** (`/Users/brandon/llmworkspace/remem`)

- Modify `src/remem/mcp_server.py` - add module state (`_pinned_project`, `_http_mode`), `configure()`, `http_run_kwargs()`, `serve_http()`. The seven tool functions are not touched.
- Modify `src/remem/cli.py:691-696` - `serve` gains four options.
- Create `tests/test_mcp_http_serve.py` - all new tests. No `db` marker; nothing here opens a database.
- Modify `CLAUDE.md` - document the transport.

**saddle** (`/Users/brandon/llmworkspace/saddle`)

- Modify `internal/profile/profile.go` - `MCP.Spawn []string`; `Expand` rewrites spawn elements.
- Create `internal/hostsvc/hostsvc.go` - start a child, wait for a TCP listener, kill it, reap by pid with a command-match guard. One responsibility: host-side child processes. Knows nothing about profiles, sessions, or remem.
- Create `internal/hostsvc/hostsvc_test.go`
- Modify `internal/session/state.go` - `State.Spawned []Spawned`.
- Modify `internal/session/up.go` - start children between steps 5 and 6; add to the unwind ladder and the post-Save defer; reap in `Down`.
- Modify `internal/session/up_test.go`, `internal/profile/profile_test.go`
- Modify `docs/MANUAL-VERIFICATION.md`, `README.md`

---

# Part A: remem

### Task 1: Pin the project, null the session id

**Repo:** `/Users/brandon/llmworkspace/remem`

**Files:**
- Modify: `src/remem/mcp_server.py` (module state near `AGENT_NAME`, plus `_default_project` and `_session_id`)
- Test: `tests/test_mcp_http_serve.py` (create)

**Interfaces:**
- Consumes: `remem.project.resolve_project`, already imported by `mcp_server.py`.
- Produces:
  - `configure(*, project: str | None = None, http: bool = False) -> None` - sets module state. Later tasks call it.
  - `_default_project() -> str | None` - unchanged name and return type.
  - `_session_id() -> str | None` - unchanged name and return type.

**Why this matters:** `_default_project()` resolves from the server process's working directory. Under stdio that is the agent's directory and correct. Under HTTP the server is a host-side process in an unrelated directory, so every write files under the wrong project - and that failure is *silent*, because the write succeeds, returns an id, and simply never appears in a knowledge base that queries on project.

- [ ] **Step 1: Write the failing tests**

Create `tests/test_mcp_http_serve.py`:

```python
from __future__ import annotations

import pytest


@pytest.fixture(autouse=True)
def reset_module_state():
    """mcp_server keeps launch configuration in module globals, so a test that
    sets them would otherwise leak into every test that runs after it."""
    from remem import mcp_server

    yield
    mcp_server.configure(project=None, http=False)


def test_stdio_derives_the_project_from_the_working_directory(monkeypatch, tmp_path):
    from remem import mcp_server

    monkeypatch.setattr(mcp_server, "resolve_project", lambda: "from-cwd")
    assert mcp_server._default_project() == "from-cwd"


def test_a_pinned_project_replaces_the_working_directory(monkeypatch):
    from remem import mcp_server

    monkeypatch.setattr(mcp_server, "resolve_project", lambda: "from-cwd")
    mcp_server.configure(project="saddle", http=True)
    assert mcp_server._default_project() == "saddle"


def test_stdio_reports_the_session_id_from_the_environment(monkeypatch):
    from remem import mcp_server

    monkeypatch.setenv("CLAUDE_SESSION_ID", "abc123")
    assert mcp_server._session_id() == "abc123"


def test_http_reports_no_session_id_even_when_the_environment_sets_one(monkeypatch):
    # The server's environment belongs to whatever launched it, not to the
    # agent making the call, so the value is actively wrong rather than
    # merely absent. Recording null is the honest answer.
    from remem import mcp_server

    monkeypatch.setenv("CLAUDE_SESSION_ID", "the-launchers-session")
    mcp_server.configure(http=True)
    assert mcp_server._session_id() is None
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/test_mcp_http_serve.py -v`
Expected: FAIL with `AttributeError: module 'remem.mcp_server' has no attribute 'configure'`

- [ ] **Step 3: Write the minimal implementation**

In `src/remem/mcp_server.py`, below `AGENT_NAME = "claude-code"`:

```python
# Set once at launch by `configure`. Under stdio both stay at their defaults
# and every behaviour below is exactly what it was before HTTP existed.
_pinned_project: str | None = None
_http_mode = False


def configure(*, project: str | None = None, http: bool = False) -> None:
    """Record how this process was launched. Call before serving."""
    global _pinned_project, _http_mode
    _pinned_project = project
    _http_mode = http
```

Change `_default_project` to consult the pin first, keeping its existing docstring and adding to it:

```python
def _default_project() -> str | None:
    """...existing docstring...

    Over HTTP the server is a host-side process whose working directory has
    nothing to do with the agent's, so the project is pinned at launch
    instead. An explicit `project` argument on a tool call still wins; this
    only supplies the default.
    """
    if _pinned_project is not None:
        return _pinned_project
    return resolve_project()
```

Change `_session_id`, keeping its existing comment and adding the second paragraph:

```python
def _session_id() -> str | None:
    # MCP tool calls carry no session id; use one only if the environment
    # supplies it. Recording null is better than fabricating a value.
    #
    # Over HTTP the environment is the launcher's, not the agent's, so the
    # variable is not merely absent but wrong. Same argument, stronger case.
    if _http_mode:
        return None
    return os.environ.get("CLAUDE_SESSION_ID")
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_mcp_http_serve.py -v`
Expected: 4 passed

- [ ] **Step 5: Confirm nothing else regressed**

Run: `uv run pytest -m 'not db' -q`
Expected: no new failures. Note the skip count; DB tests skip when Postgres is down and a green run does not mean they ran.

- [ ] **Step 6: Commit**

```bash
git add tests/test_mcp_http_serve.py src/remem/mcp_server.py
git commit -m "Pin the MCP server's project and null its session id over HTTP"
```

---

### Task 2: The HTTP listener

**Repo:** `/Users/brandon/llmworkspace/remem`

**Files:**
- Modify: `src/remem/mcp_server.py` (add `http_run_kwargs`, `serve_http`; leave `main` alone)
- Test: `tests/test_mcp_http_serve.py` (append)

**Interfaces:**
- Consumes: `configure()` from Task 1.
- Produces:
  - `http_run_kwargs(host: str, port: int) -> dict` - the keyword arguments for `mcp.run("streamable-http", ...)`. Split out from `serve_http` purely so it can be asserted without binding a socket.
  - `serve_http(host: str, port: int, project: str | None) -> None` - configures, then blocks serving.
  - `main() -> None` - unchanged, still stdio.

**Why the split:** a test that called `serve_http` would bind a port and block forever. Testing the settings object instead gets the whole risk - the allowlist and the stateless flag - with no socket and no timeout.

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_mcp_http_serve.py`:

```python
def test_the_listener_allowlists_exactly_the_bound_address():
    # enable_dns_rebinding_protection defaults to True, so an empty or wrong
    # allowed_hosts rejects every request the container makes. This assertion
    # is the difference between the feature working and silently refusing.
    from remem.mcp_server import http_run_kwargs

    kw = http_run_kwargs("192.168.64.3", 9100)
    assert kw["host"] == "192.168.64.3"
    assert kw["port"] == 9100
    assert kw["transport_security"].allowed_hosts == ["192.168.64.3:9100"]
    assert kw["transport_security"].allowed_origins == []
    assert kw["transport_security"].enable_dns_rebinding_protection is True


def test_the_listener_is_stateless():
    # Every tool opens its own session and holds nothing between calls, so
    # there is no server-side state a session id would protect - and stateless
    # means a container reconnecting after a restart cannot land on an
    # expired session.
    from remem.mcp_server import http_run_kwargs

    assert http_run_kwargs("127.0.0.1", 9100)["stateless_http"] is True


def test_serving_http_pins_the_project_before_it_blocks(monkeypatch):
    from remem import mcp_server

    seen = {}

    def fake_run(transport, **kwargs):
        seen["transport"] = transport
        seen["kwargs"] = kwargs
        seen["project"] = mcp_server._default_project()

    monkeypatch.setattr(mcp_server.mcp, "run", fake_run)
    mcp_server.serve_http("192.168.64.3", 9100, "saddle")

    assert seen["transport"] == "streamable-http"
    assert seen["project"] == "saddle"
    assert seen["kwargs"]["host"] == "192.168.64.3"


def test_main_still_serves_stdio(monkeypatch):
    from remem import mcp_server

    seen = {}
    monkeypatch.setattr(mcp_server.mcp, "run", lambda *a, **k: seen.update(args=a, kwargs=k))
    mcp_server.main()

    assert seen["args"] == ()
    assert seen["kwargs"] == {}
    assert mcp_server._http_mode is False
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/test_mcp_http_serve.py -v`
Expected: FAIL with `ImportError: cannot import name 'http_run_kwargs'`

- [ ] **Step 3: Write the minimal implementation**

Add the import at the top of `src/remem/mcp_server.py`, beside the existing `MCPServer` import:

```python
from mcp.server.transport_security import TransportSecuritySettings
```

Add above `def main()`:

```python
def http_run_kwargs(host: str, port: int) -> dict:
    """Keyword arguments for serving over streamable-HTTP.

    Separate from `serve_http` so the security-relevant parts can be asserted
    without binding a socket.
    """
    return {
        "host": host,
        "port": port,
        "stateless_http": True,
        # Host-header validation, which the SDK enables by default to stop DNS
        # rebinding. It is not access control and is not claimed as any - the
        # boundary is the network saddle puts the container on. But binding to
        # a gateway address means that address must be allowlisted or every
        # request is refused.
        "transport_security": TransportSecuritySettings(
            allowed_hosts=[f"{host}:{port}"],
            allowed_origins=[],
        ),
    }


def serve_http(host: str, port: int, project: str | None) -> None:
    """Serve over streamable-HTTP. Blocks until the server stops."""
    configure(project=project, http=True)
    mcp.run("streamable-http", **http_run_kwargs(host, port))
```

Leave `main()` exactly as it is.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_mcp_http_serve.py -v`
Expected: 8 passed

- [ ] **Step 5: Commit**

```bash
git add tests/test_mcp_http_serve.py src/remem/mcp_server.py
git commit -m "Serve the MCP tools over streamable-HTTP"
```

---

### Task 3: The `serve` flags, and the docs

**Repo:** `/Users/brandon/llmworkspace/remem`

**Files:**
- Modify: `src/remem/cli.py:691-696`
- Modify: `CLAUDE.md`
- Test: `tests/test_mcp_http_serve.py` (append)

**Interfaces:**
- Consumes: `serve_http(host, port, project)` and `main()` from Task 2.
- Produces: the `remem serve` command surface. saddle's Task 7 profile calls it verbatim.

**Command surface:**

```
remem serve                                             # stdio, unchanged
remem serve --http --host H --port P --project NAME     # streamable-HTTP
```

`--host` defaults to `127.0.0.1`, `--port` to `9100`. Without `--http` the new flags are inert. `--project` is accepted in both modes and simply pins attribution.

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_mcp_http_serve.py`:

```python
def _invoke(monkeypatch, argv):
    from typer.testing import CliRunner

    from remem.cli import app

    return CliRunner().invoke(app, argv)


def test_bare_serve_runs_stdio(monkeypatch):
    from remem import mcp_server

    called = {}
    monkeypatch.setattr(mcp_server, "main", lambda: called.setdefault("stdio", True))
    monkeypatch.setattr(mcp_server, "serve_http", lambda *a: called.setdefault("http", a))

    result = _invoke(monkeypatch, ["serve"])
    assert result.exit_code == 0
    assert called == {"stdio": True}


def test_serve_http_passes_host_port_and_project(monkeypatch):
    from remem import mcp_server

    called = {}
    monkeypatch.setattr(mcp_server, "main", lambda: called.setdefault("stdio", True))
    monkeypatch.setattr(mcp_server, "serve_http", lambda *a: called.setdefault("http", a))

    result = _invoke(
        monkeypatch,
        ["serve", "--http", "--host", "192.168.64.3", "--port", "9100",
         "--project", "saddle"],
    )
    assert result.exit_code == 0
    assert called == {"http": ("192.168.64.3", 9100, "saddle")}


def test_serve_http_defaults_to_loopback_and_9100(monkeypatch):
    from remem import mcp_server

    called = {}
    monkeypatch.setattr(mcp_server, "serve_http", lambda *a: called.setdefault("http", a))

    result = _invoke(monkeypatch, ["serve", "--http"])
    assert result.exit_code == 0
    assert called == {"http": ("127.0.0.1", 9100, None)}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `uv run pytest tests/test_mcp_http_serve.py -v -k serve`
Expected: FAIL - `--http` is not a known option, exit code 2.

- [ ] **Step 3: Write the minimal implementation**

Replace `src/remem/cli.py:691-696` with:

```python
@app.command()
def serve(
    http: Annotated[bool, typer.Option("--http")] = False,
    host: Annotated[str, typer.Option("--host")] = "127.0.0.1",
    port: Annotated[int, typer.Option("--port")] = 9100,
    project: Annotated[str | None, typer.Option("--project")] = None,
):
    """Run the MCP server.

    Stdio by default, which is what an agent on this machine launches.
    `--http` serves streamable-HTTP instead, for an agent that cannot start a
    local process - a container, or a remote host. Over HTTP the working
    directory is the server's, not the agent's, so pass `--project` or writes
    file under the wrong project and never surface again.
    """
    from remem import mcp_server

    if http:
        mcp_server.serve_http(host, port, project)
    else:
        mcp_server.main()
```

`Annotated` and `typer` are already imported at the top of `cli.py`; confirm rather than re-adding.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_mcp_http_serve.py -v`
Expected: 11 passed

- [ ] **Step 5: Check the whole suite and the help text**

Run: `uv run pytest -m 'not db' -q` - no new failures.
Run: `uv run remem serve --help` - confirm all four options appear.

- [ ] **Step 6: Document it**

In `CLAUDE.md`, add a section near the other command documentation:

```markdown
## `remem serve --http`

`remem serve` is stdio by default: the agent starts the process, so the
process's working directory is the agent's and `_default_project()` resolves
correctly from it.

`--http` serves the same seven tools over streamable-HTTP, for an agent that
cannot start a local process - a container, or a remote host. Two things
change, both in `mcp_server.py`:

- The project is pinned by `--project` instead of derived from the working
  directory. Without it, every write files under whatever directory the server
  was launched in, succeeds, and never appears in a knowledge base again.
- The session id is null. `CLAUDE_SESSION_ID` in the server's environment
  belongs to whatever launched it, not to the agent calling the tool.

The listener is stateless and allowlists exactly the `host:port` it bound.
That allowlist is load-bearing: the SDK enables DNS-rebinding protection by
default, so a non-loopback bind without it refuses every request.

There is no authentication. Do not bind this to an address you do not control
the network of. saddle's use binds it to a per-session `--internal` network's
gateway, which has no internet route.
```

- [ ] **Step 7: Commit**

```bash
git add src/remem/cli.py tests/test_mcp_http_serve.py CLAUDE.md
git commit -m "Add remem serve --http, --host, --port, --project"
```

---

# Part B: saddle

### Task 4: `carry_in.mcp.<name>.spawn` in the profile

**Repo:** `/Users/brandon/llmworkspace/saddle`

**Files:**
- Modify: `internal/profile/profile.go:23-26` (the `MCP` struct) and the `Expand` function
- Test: `internal/profile/profile_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `profile.MCP{URL string, Tools string, Spawn []string}` with yaml tag `spawn`.
  - `profile.Expand(p Profile, vars map[string]string) Profile` - now rewrites every element of each `Spawn` as well as each `URL`. Signature unchanged.

**Note on `{{repo}}`:** `Expand` takes an arbitrary `vars` map, so no code change is needed to support a new variable name. Task 6 passes `repo`. This task only makes `Spawn` elements subject to expansion at all.

- [ ] **Step 1: Write the failing tests**

Append to `internal/profile/profile_test.go`:

```go
func TestSpawnParses(t *testing.T) {
	p, err := Parse([]byte(`
name: go
carry_in:
  mcp:
    remem:
      spawn: ["remem", "serve", "--http", "--host", "{{gateway}}"]
      url: http://{{gateway}}:9100/mcp
`))
	if err != nil {
		t.Fatal(err)
	}
	got := p.CarryIn.MCP["remem"].Spawn
	want := []string{"remem", "serve", "--http", "--host", "{{gateway}}"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestExpandRewritesSpawnArgs(t *testing.T) {
	p := Profile{CarryIn: CarryIn{MCP: map[string]MCP{"remem": {
		URL:   "http://{{gateway}}:9100/mcp",
		Spawn: []string{"remem", "serve", "--host", "{{gateway}}", "--project", "{{repo}}"},
	}}}}
	out := Expand(p, map[string]string{"gateway": "192.168.64.3", "repo": "saddle"})

	got := out.CarryIn.MCP["remem"].Spawn
	want := []string{"remem", "serve", "--host", "192.168.64.3", "--project", "saddle"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: got %q want %q", i, got[i], want[i])
		}
	}
	if out.CarryIn.MCP["remem"].URL != "http://192.168.64.3:9100/mcp" {
		t.Fatalf("url not expanded: %q", out.CarryIn.MCP["remem"].URL)
	}
}

func TestExpandDoesNotMutateTheInputSpawn(t *testing.T) {
	// Expand returns a copy. A shared backing array would let one session's
	// gateway leak into another profile value.
	orig := []string{"remem", "--host", "{{gateway}}"}
	p := Profile{CarryIn: CarryIn{MCP: map[string]MCP{"remem": {Spawn: orig}}}}
	Expand(p, map[string]string{"gateway": "192.168.64.3"})
	if orig[2] != "{{gateway}}" {
		t.Fatalf("input mutated: %q", orig[2])
	}
}

func TestProfileWithURLAndNoSpawnIsValid(t *testing.T) {
	p, err := Parse([]byte(`
name: go
carry_in:
  mcp:
    other:
      url: http://{{gateway}}:7000/mcp
`))
	if err != nil {
		t.Fatal(err)
	}
	if p.CarryIn.MCP["other"].Spawn != nil {
		t.Fatal("expected no spawn")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/profile/ -run 'Spawn' -v`
Expected: FAIL to compile - `unknown field Spawn in struct literal`.

- [ ] **Step 3: Write the minimal implementation**

In `internal/profile/profile.go`, extend the struct:

```go
type MCP struct {
	URL   string `yaml:"url"`
	Tools string `yaml:"tools"`
	// Spawn is the host command that serves this endpoint, run by saddle
	// once the session gateway exists. Optional: a server with only a URL is
	// assumed to be running already.
	Spawn []string `yaml:"spawn"`
}
```

In `Expand`, inside the existing `for name, s := range p.CarryIn.MCP` loop, after the `s.URL` line:

```go
		s.URL = r.Replace(s.URL)
		if s.Spawn != nil {
			// A fresh slice: the caller's Profile must not be mutated, and a
			// shared backing array would let one session's gateway leak into
			// another's arguments.
			spawn := make([]string, len(s.Spawn))
			for i, a := range s.Spawn {
				spawn[i] = r.Replace(a)
			}
			s.Spawn = spawn
		}
		m[name] = s
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/profile/ -v`
Expected: PASS, including the pre-existing tests.

- [ ] **Step 5: Commit**

```bash
git add internal/profile/profile.go internal/profile/profile_test.go
git commit -m "Add carry_in.mcp.<name>.spawn to the profile schema"
```

---

### Task 5: The `hostsvc` package

**Repo:** `/Users/brandon/llmworkspace/saddle`

**Files:**
- Create: `internal/hostsvc/hostsvc.go`
- Create: `internal/hostsvc/hostsvc_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks. This package knows nothing about profiles, sessions, or remem.
- Produces:
  - `type Proc struct { PID int; Cmd []string }`
  - `func Start(argv []string) (*Proc, error)` - starts a child. Errors if `argv` is empty or the binary is not found.
  - `func (p *Proc) Kill() error` - terminates the child and reaps it.
  - `func WaitReady(addr string, timeout time.Duration) error` - polls TCP connect to `addr` until it succeeds or the timeout expires.
  - `func AddrFromURL(raw string) (string, error)` - `"http://192.168.64.3:9100/mcp"` becomes `"192.168.64.3:9100"`.
  - `func Reap(pid int, cmd []string) error` - kills `pid` **only** if its current command line matches `cmd`. For `Down`, running in a different process from the one that spawned the child.

**Why `Reap` checks the command:** a pid alone is reused. `Down` runs from a state file that may be minutes or days old, and killing a recycled pid would terminate an unrelated process of the user's.

- [ ] **Step 1: Write the failing tests**

Create `internal/hostsvc/hostsvc_test.go`:

```go
package hostsvc

import (
	"net"
	"os/exec"
	"testing"
	"time"
)

func TestAddrFromURL(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"http://192.168.64.3:9100/mcp", "192.168.64.3:9100"},
		{"http://127.0.0.1:9100/mcp", "127.0.0.1:9100"},
	} {
		got, err := AddrFromURL(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%s: got %q want %q", c.in, got, c.want)
		}
	}
}

func TestAddrFromURLRejectsAURLWithNoPort(t *testing.T) {
	// A spawned server's readiness check needs a concrete port. Defaulting to
	// 80 would poll the wrong socket and report ready when nothing is there.
	if _, err := AddrFromURL("http://192.168.64.3/mcp"); err == nil {
		t.Fatal("expected an error for a URL with no port")
	}
}

func TestStartRunsTheCommandAndKillStopsIt(t *testing.T) {
	p, err := Start([]string{"sleep", "30"})
	if err != nil {
		t.Fatal(err)
	}
	if p.PID <= 0 {
		t.Fatalf("bad pid %d", p.PID)
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	// A killed and reaped process is no longer signalable.
	if err := exec.Command("kill", "-0", itoa(p.PID)).Run(); err == nil {
		t.Fatal("process still alive after Kill")
	}
}

func TestStartRejectsAnEmptyCommand(t *testing.T) {
	if _, err := Start(nil); err == nil {
		t.Fatal("expected an error for an empty command")
	}
}

func TestStartFailsWhenTheBinaryDoesNotExist(t *testing.T) {
	if _, err := Start([]string{"saddle-no-such-binary-xyz"}); err == nil {
		t.Fatal("expected an error for a missing binary")
	}
}

func TestWaitReadyReturnsOnceSomethingIsListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := WaitReady(ln.Addr().String(), 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestWaitReadyTimesOutWhenNothingBinds(t *testing.T) {
	// Bind and immediately close, so the port is almost certainly free.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	start := time.Now()
	if err := WaitReady(addr, 300*time.Millisecond); err == nil {
		t.Fatal("expected a timeout")
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Fatal("returned before the timeout elapsed")
	}
}

func TestReapKillsAMatchingProcess(t *testing.T) {
	p, err := Start([]string{"sleep", "30"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	if err := Reap(p.PID, p.Cmd); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("kill", "-0", itoa(p.PID)).Run(); err == nil {
		t.Fatal("process still alive after Reap")
	}
}

func TestReapRefusesWhenTheCommandDoesNotMatch(t *testing.T) {
	// The pid-reuse guard. Down runs from a state file that may be old; a
	// recycled pid must never be killed.
	p, err := Start([]string{"sleep", "30"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()

	if err := Reap(p.PID, []string{"some-other-command"}); err == nil {
		t.Fatal("expected Reap to refuse a mismatched command")
	}
	if err := exec.Command("kill", "-0", itoa(p.PID)).Run(); err != nil {
		t.Fatal("Reap killed a process whose command did not match")
	}
}

func TestReapOnAPidThatIsGoneIsNotAnError(t *testing.T) {
	// Teardown of an already-dead child is the normal case, not a failure.
	if err := Reap(999999, []string{"sleep", "30"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
```

Add the small helper at the bottom of the test file:

```go
func itoa(i int) string { return strconv.Itoa(i) }
```

and `"strconv"` to the test file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/hostsvc/ -v`
Expected: FAIL - the package does not exist.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/hostsvc/hostsvc.go`:

```go
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
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/hostsvc/ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/hostsvc/
git commit -m "Add hostsvc: start, await, and reap host-side child processes"
```

---

### Task 6: Wire spawning into the session lifecycle

**Repo:** `/Users/brandon/llmworkspace/saddle`

**Files:**
- Modify: `internal/session/state.go` (the `State` struct)
- Modify: `internal/session/up.go` (`Up`'s unwind ladder, a new step 5b, the post-Save defer, and `Down`)
- Test: `internal/session/up_test.go`

**Interfaces:**
- Consumes: `profile.MCP.Spawn` (Task 4); `hostsvc.Start`, `hostsvc.WaitReady`, `hostsvc.AddrFromURL`, `hostsvc.Reap`, `hostsvc.Proc` (Task 5).
- Produces:
  - `session.Spawned struct { Name string; PID int; Cmd []string }` with json tags `name`, `pid`, `cmd`.
  - `State.Spawned []Spawned` with json tag `spawned`.
  - `session.SpawnReadyTimeout = 30 * time.Second` - a package-level var so a test can shorten it.
  - `session.startSpawned(p profile.Profile, timeout time.Duration) ([]*hostsvc.Proc, []Spawned, error)` - starts every server in `p.CarryIn.MCP` that has a `Spawn`, waits for each, and returns both the live handles (for the unwind) and the recordable descriptions (for `State`). On any failure it kills whatever it already started and returns the error.

**Ordering:** step 5b, after the network exists so `{{gateway}}` is known and after `profile.Expand`, and before the container starts so nothing races the bind. Note this requires moving the existing `Expand` call (step 6) above the spawn, since spawn arguments need expanding first. `Expand` already gets `gateway` and `proxy_port`; add `repo`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/session/up_test.go`:

```go
func TestStartSpawnedRunsNothingWhenNoServerDeclaresSpawn(t *testing.T) {
	p := profile.Profile{CarryIn: profile.CarryIn{MCP: map[string]profile.MCP{
		"other": {URL: "http://127.0.0.1:9100/mcp"},
	}}}
	procs, rec, err := startSpawned(p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 0 || len(rec) != 0 {
		t.Fatalf("started something: %v %v", procs, rec)
	}
}

func TestStartSpawnedWaitsForTheServerToBind(t *testing.T) {
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	p := profile.Profile{CarryIn: profile.CarryIn{MCP: map[string]profile.MCP{
		"fake": {
			URL:   "http://" + addr + "/mcp",
			// A listener that binds and holds, with no dependence on which
			// netcat macOS ships. Started as a child exactly as a real
			// server would be.
			Spawn: listenerCmd(port),
		},
	}}}

	procs, rec, err := startSpawned(p, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, pr := range procs {
			_ = pr.Kill()
		}
	}()

	if len(procs) != 1 {
		t.Fatalf("want 1 process, got %d", len(procs))
	}
	if len(rec) != 1 || rec[0].Name != "fake" || rec[0].PID != procs[0].PID {
		t.Fatalf("bad record %+v", rec)
	}
}

func TestStartSpawnedFailsWhenTheServerNeverBinds(t *testing.T) {
	port := freePort(t)
	p := profile.Profile{CarryIn: profile.CarryIn{MCP: map[string]profile.MCP{
		"fake": {
			URL:   fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
			Spawn: []string{"sleep", "30"},
		},
	}}}

	procs, _, err := startSpawned(p, 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected a readiness failure")
	}
	if len(procs) != 0 {
		t.Fatal("a failed start must leave nothing running")
	}
}

func TestStartSpawnedFailsWhenTheBinaryIsMissing(t *testing.T) {
	p := profile.Profile{CarryIn: profile.CarryIn{MCP: map[string]profile.MCP{
		"fake": {
			URL:   "http://127.0.0.1:9100/mcp",
			Spawn: []string{"saddle-no-such-binary-xyz"},
		},
	}}}
	if _, _, err := startSpawned(p, time.Second); err == nil {
		t.Fatal("expected an error for a missing binary")
	}
}

func TestStartSpawnedRejectsASpawnWithNoUsableURL(t *testing.T) {
	// spawn without a port to poll cannot be waited on, and a server that is
	// started but never checked is exactly the silent half-working state the
	// design refuses.
	p := profile.Profile{CarryIn: profile.CarryIn{MCP: map[string]profile.MCP{
		"fake": {URL: "http://127.0.0.1/mcp", Spawn: []string{"sleep", "30"}},
	}}}
	if _, _, err := startSpawned(p, time.Second); err == nil {
		t.Fatal("expected an error for a URL with no port")
	}
}

func TestStateRoundTripsSpawnedProcesses(t *testing.T) {
	st := State{
		Name: "saddle-x", Repo: "/r", Worktree: "/w", Branch: "b",
		Spawned: []Spawned{{Name: "remem", PID: 4242, Cmd: []string{"remem", "serve"}}},
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var back State
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Spawned) != 1 || back.Spawned[0].PID != 4242 ||
		back.Spawned[0].Cmd[0] != "remem" {
		t.Fatalf("bad round trip: %+v", back.Spawned)
	}
}

// listenerCmd binds port and sleeps, standing in for a spawned MCP server.
func listenerCmd(port int) []string {
	return []string{"python3", "-c",
		"import socket,time;s=socket.socket();" +
			"s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);" +
			"s.bind(('127.0.0.1'," + strconv.Itoa(port) + "));s.listen();time.sleep(30)"}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
```

Add `"fmt"`, `"net"`, `"strconv"`, `"time"` to the test file's imports if not present.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/session/ -run 'Spawn' -v`
Expected: FAIL to compile - `undefined: startSpawned`, `unknown field Spawned`.

- [ ] **Step 3: Add the state**

In `internal/session/state.go`, above `type State struct`:

```go
// Spawned records a host-side child process started for this session, so a
// later `saddle down` - a different process from the `up` that started it -
// can reap a survivor of a crashed `up`. Cmd is recorded alongside Pid
// because a pid on its own is reused and is not identity.
type Spawned struct {
	Name string   `json:"name"`
	PID  int      `json:"pid"`
	Cmd  []string `json:"cmd"`
}
```

and inside `State`, after `ProxyAddr`:

```go
	Spawned []Spawned `json:"spawned"`
```

- [ ] **Step 4: Implement `startSpawned`**

In `internal/session/up.go`, add near the other helpers:

```go
// SpawnReadyTimeout bounds how long a spawned host service has to bind.
// A var, not a const, so tests need not wait it out.
var SpawnReadyTimeout = 30 * time.Second

// startSpawned starts every carried-in MCP server that declares a spawn
// command, and does not return until each is accepting connections.
//
// Waiting is the point. The egress proxy binds synchronously, so nothing can
// race it; a child process does not, and a container started before the
// server binds gives the agent an MCP server that does not exist. A session
// that looks contained and memory-backed but is only the first of those is
// exactly what saddle refuses to produce, so a server that never binds fails
// `up` rather than being shrugged off.
//
// The profile must already have been expanded: these arguments carry the
// session gateway.
func startSpawned(p profile.Profile, timeout time.Duration) ([]*hostsvc.Proc, []Spawned, error) {
	var procs []*hostsvc.Proc
	var rec []Spawned

	fail := func(err error) ([]*hostsvc.Proc, []Spawned, error) {
		for _, pr := range procs {
			_ = pr.Kill()
		}
		return nil, nil, err
	}

	// Map iteration order is random; sort so a failure is reproducible and
	// two runs of the same profile start servers in the same order.
	names := make([]string, 0, len(p.CarryIn.MCP))
	for name := range p.CarryIn.MCP {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		m := p.CarryIn.MCP[name]
		if len(m.Spawn) == 0 {
			continue
		}
		addr, err := hostsvc.AddrFromURL(m.URL)
		if err != nil {
			return fail(fmt.Errorf("carry_in.mcp.%s: %w", name, err))
		}
		pr, err := hostsvc.Start(m.Spawn)
		if err != nil {
			return fail(fmt.Errorf("carry_in.mcp.%s: %w", name, err))
		}
		procs = append(procs, pr)
		if err := hostsvc.WaitReady(addr, timeout); err != nil {
			return fail(fmt.Errorf("carry_in.mcp.%s: %w", name, err))
		}
		rec = append(rec, Spawned{Name: name, PID: pr.PID, Cmd: pr.Cmd})
	}
	return procs, rec, nil
}
```

Add `"sort"` and `"github.com/brandon/saddle/internal/hostsvc"` to `up.go`'s imports.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/session/ -run 'Spawn|StateRoundTrips' -v`
Expected: PASS. `TestStartSpawnedWaitsForTheServerToBind` uses `python3`, present on macOS.

- [ ] **Step 6: Wire it into `Up`**

Three edits in `up.go`.

First, the unwind ladder. Add to the `var (...)` block:

```go
		spawned     []*hostsvc.Proc
```

and inside the `defer func()`, between the container removal and `px.Close()` - children go after the container, so nothing is torn out from under a container that still exists:

```go
		for _, pr := range spawned {
			_ = pr.Kill()
		}
```

Second, move the existing step 6 `profile.Expand` call above the spawn, add `repo`, and start the children. The block becomes:

```go
	// 6. Expand profile placeholders now that the gateway is known.
	p = profile.Expand(p, map[string]string{
		"gateway":    n.Gateway,
		"proxy_port": proxyPort,
		"repo":       filepath.Base(o.Repo),
	})

	// 6b. Host services the profile asks for, before the container so that
	// nothing races their bind. See startSpawned for why this waits.
	//
	// `spawned` is the ladder variable declared above, so this assigns
	// rather than declares: a `:=` here would shadow it and the unwind
	// would kill nothing.
	var spawnedRec []Spawned
	spawned, spawnedRec, err = startSpawned(p, SpawnReadyTimeout)
	if err != nil {
		return State{}, err
	}
```

Third, record and hold them. Add to the `State` literal at step 10:

```go
		Spawned: spawnedRec,
```

and extend the post-Save defer that already closes the proxy:

```go
	defer func() {
		if px != nil {
			px.Close()
		}
		for _, pr := range spawned {
			_ = pr.Kill()
		}
	}()
```

- [ ] **Step 7: Reap in `Down`**

In `Down`, after the network is deleted and before the worktree is removed:

```go
	// Children of the `up` process. Normally already dead, because `up`
	// kills them on the way out; this catches a survivor of a crashed `up`.
	// Reap refuses to kill a pid whose command no longer matches, so a
	// recycled pid is never mistaken for our child.
	for _, s := range st.Spawned {
		if err := hostsvc.Reap(s.PID, s.Cmd); err != nil && !force {
			return err
		}
	}
```

- [ ] **Step 8: Run the full suite**

Run: `go test ./... -v`
Expected: all PASS.
Run: `go vet ./...`
Expected: clean.

- [ ] **Step 9: Commit**

```bash
git add internal/session/state.go internal/session/up.go internal/session/up_test.go
git commit -m "Start, await, and reap profile-declared host services in a session"
```

---

### Task 7: The remem profile entry and the docs

**Repo:** `/Users/brandon/llmworkspace/saddle`

**Files:**
- Modify: `README.md` (the example profile at line 29, and the profiles section)
- Modify: `docs/superpowers/specs/2026-09-05-saddle-design.md`
- Modify: `docs/MANUAL-VERIFICATION.md`

**Interfaces:**
- Consumes: everything from Tasks 1-6. The `spawn` line must match Task 3's CLI surface exactly.

**The repo ships no profiles.** `config.ProfilesDir()` resolves to
`~/.config/saddle/profiles`, `LoadProfiles` returns none when it is missing,
and the only profile in the repository is the example in `README.md:29`. So
this task updates that example; the operator updates their own profile by
hand, and `docs/MANUAL-VERIFICATION.md` is where that gets confirmed.

- [ ] **Step 1: Update the example profile in the README**

In the `README.md:29` heredoc that writes `~/.config/saddle/profiles/go.yaml`:

```yaml
carry_in:
  skills: [remem, superpowers]
  mcp:
    remem:
      spawn: ["remem", "serve", "--http",
              "--host", "{{gateway}}", "--port", "9100",
              "--project", "{{repo}}"]
      url: http://{{gateway}}:9100/mcp
```

- [ ] **Step 2: Verify the example profile parses and expands**

The README example is the only profile under version control, and a broken
one sends every new user into a failing `saddle up`. Add a test that parses
it, in `internal/profile/profile_test.go`:

```go
func TestTheREADMEExampleProfileParses(t *testing.T) {
	// The README is the only profile in the repository. If its spawn line
	// drifts from `remem serve`'s actual flags, every new user's first
	// session fails.
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(data), "profiles/go.yaml <<'YAML'\n")
	if !ok {
		t.Fatal("could not find the example profile in README.md")
	}
	body, _, ok = strings.Cut(body, "\nYAML")
	if !ok {
		t.Fatal("unterminated example profile in README.md")
	}

	p, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("README example does not parse: %v", err)
	}
	spawn := p.CarryIn.MCP["remem"].Spawn
	if len(spawn) == 0 || spawn[0] != "remem" {
		t.Fatalf("README example lost its remem spawn: %v", spawn)
	}
	out := Expand(p, map[string]string{"gateway": "10.0.0.1", "repo": "r", "proxy_port": "1"})
	for _, a := range out.CarryIn.MCP["remem"].Spawn {
		if strings.Contains(a, "{{") {
			t.Fatalf("unexpanded placeholder in %q", a)
		}
	}
}
```

Add `"os"` and `"strings"` to the test file's imports if not present.

Run: `go test ./internal/profile/ -v`
Expected: PASS.

- [ ] **Step 3: Correct the superseded claims in the saddle design doc**

`docs/superpowers/specs/2026-09-05-saddle-design.md` now contains two
statements that are no longer true. Leaving stale claims in the design
document of a security tool is the same failure as a profile field that
parses and does nothing: a reader cannot tell what is current.

In the "Profiles and carry-in" section, replace:

> A carried-in MCP server's own port is written literally by the profile
> author, because saddle does not start that server and cannot know its port.

with:

> A carried-in MCP server's port is written literally by the profile author.
> saddle can now start such a server - see `carry_in.mcp.<name>.spawn` and
> `2026-09-06-remem-http-transport-design.md` - but a fixed port needs no
> negotiating, because each session's gateway is a distinct address.

In the `### remem` subsection, replace the sentence describing the store as
SQLite in `~/Library/Application Support/remem` and the clause about "a host
process and a container process racing" as writers, with:

> remem's store is Postgres (`session.py` connects with psycopg to a
> configured DSN), reachable from the host but not from the container. The
> container never gets file access to the knowledge base, and concurrent
> writers are Postgres's ordinary business.

Also mark the stdio-only paragraph as resolved, pointing at the new design.

- [ ] **Step 4: Document it in the README**

Add to the profiles section:

```markdown
`carry_in.mcp.<name>.spawn` is the host command that serves that endpoint.
saddle runs it once the session network exists, waits for the address in
`url` to accept connections, and kills it when the session ends. A server
with only a `url` is assumed to be running already.

`spawn` runs a host command, outside the container, as you. That is not a new
trust boundary - a profile already chooses the container image and the bind
mounts - but it is worth knowing before you copy a profile from someone else.

If the command does not exist, or nothing is listening within 30 seconds,
`saddle up` fails. A session whose memory server silently is not there is
worse than one that refuses to start.
```

- [ ] **Step 5: Add the manual verification items**

Append to `docs/MANUAL-VERIFICATION.md`:

```markdown
## Carried-in remem over HTTP

- [ ] `saddle up` on a repo whose profile carries remem in. From inside the
      session, call `recall` and confirm results come back.
- [ ] From inside the session, write an entry with no explicit `project`.
      On the host, confirm it filed under the repository's name and not under
      the directory `saddle up` was run from. This is the silent failure the
      pinned project exists to prevent, so check it rather than assume it.
- [ ] Confirm the entry's session id is null rather than the launching
      shell's.
- [ ] `saddle down`, then `ps ax | grep 'remem serve'` - no survivor.
- [ ] Kill the `saddle up` process with SIGKILL rather than exiting cleanly,
      so the child is orphaned. Then `saddle down` and confirm it reaps the
      orphan.
- [ ] Two sessions at once on different repos: confirm both remem servers
      bind :9100 on their own gateways without colliding, and that each files
      entries under its own project.
- [ ] A profile whose `spawn` names a nonexistent binary fails `saddle up`
      with a clear error and leaves no network, worktree, or container behind.
```

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "Carry remem in over HTTP; document spawn and its manual checks"
```

---

## Definition of Done

- `uv run pytest -m 'not db'` passes in remem; `remem serve` with no flags is unchanged.
- `go test ./...` and `go vet ./...` pass in saddle.
- A profile with `spawn` starts a server, waits for it, and reaps it; a profile with only `url` does not.
- `docs/MANUAL-VERIFICATION.md` carries the seven items above, unchecked. **They are not done by this plan.** The end-to-end path has not been exercised against a real container until a human works them.

## Out of scope

Both are recorded in the spec and neither is fixed here:

- **Automatic event recording inside containers.** `remem install`'s hook shells out to the `remem` binary, which does not exist in a Linux container. Contained sessions get the seven tools and no automatic recording.
- **`saddle attach` after `up` has exited.** The existing gotcha now covers the memory server too. Fixing it means persisting and rebinding host-side services, which belongs with parallel sessions.
