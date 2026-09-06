# remem over HTTP, and host services saddle starts

Design, 2026-09-06.

Supersedes two rulings in `2026-09-05-saddle-design.md`; see "Changes to the
saddle design" below.

## Problem

A contained session gets no remem. `remem serve` speaks stdio only, and stdio
means the agent's own process starts the server - which a container cannot do,
because remem is a macOS binary talking to a Postgres instance on the host.
saddle's `carry_in.mcp.<name>.url` already renders an HTTP endpoint into the
container's MCP config, so the container side has been ready since the first
slice. Nothing is listening at the other end.

Two changes close the gap. remem learns streamable-HTTP transport. saddle learns
to start a host-side service on the session gateway and tear it down again.

The second change is deliberately general. saddle does not learn about remem.

## Goals

1. A contained session reaches remem with all seven tools, read-write,
   attributed to the right project.
2. saddle can start any host-side MCP server a profile names, not remem
   specifically.
3. Today's stdio behaviour is unchanged, byte for byte, for every existing
   caller.

## Non-goals

- Authentication on the remem endpoint. The per-session `--internal` network
  is the boundary. See "Trust".
- Automatic event recording inside containers. Still broken, still deferred;
  see "What this does not fix".
- Persisting the server across `saddle up` exiting. It dies with `up`, exactly
  as the egress proxy does.
- A general host-process supervisor. One child per named server, started once,
  killed at teardown. No restarts, no health loop, no backoff.

## remem: the transport

`mcp` 2.1.1 already ships the transport. `MCPServer.run` takes
`transport="streamable-http"` and forwards keyword arguments to
`run_streamable_http_async(host, port, streamable_http_path, json_response,
stateless_http, event_store, retry_interval, max_request_body_size,
transport_security)`. This is a transport swap, not a rewrite. The seven tool
functions are not touched.

### CLI

```
remem serve                                             # stdio, unchanged
remem serve --http --host H --port P --project NAME     # streamable-HTTP
```

`--host` defaults to `127.0.0.1` and `--port` to `9100`. Without `--http` the
command behaves as it does today; the new flags are inert. `--project` is
accepted in both modes and simply pins attribution.

### Attribution

`_default_project()` resolves the project from the **server process's** working
directory. That is correct under stdio, where Claude Code starts the server in
the session's directory, and wrong under HTTP, where the server is a host-side
process in whatever directory it was launched from.

The failure is silent, which is what makes it worth this much attention: a
write with the wrong project succeeds, returns an id, and simply never appears
in the knowledge base, because the knowledge base queries on project. An agent
gets no signal that its memory is going nowhere.

So the project is pinned at launch:

```python
_pinned_project: str | None = None

def _default_project() -> str | None:
    return _pinned_project or resolve_project()
```

An explicit `project` argument on a tool call still wins, as it does today.
The pin only replaces the cwd-derived default.

### Session id

`_session_id()` reads `CLAUDE_SESSION_ID` from the server's environment. Over
HTTP that variable, if set at all, belongs to whatever shell launched the
server - not to the agent making the call. It returns `None` in HTTP mode.

This extends the reasoning already in that function's comment. Recording null
beats fabricating a value; over HTTP the environment's value is not merely
absent but actively wrong, which is the stronger case of the same argument.

### Listener

```python
mcp.run(
    "streamable-http",
    host=host,
    port=port,
    stateless_http=True,
    transport_security=TransportSecuritySettings(
        allowed_hosts=[f"{host}:{port}"],
        allowed_origins=[],
    ),
)
```

`stateless_http=True` because every tool opens its own `open_session()` and
holds nothing between calls. There is no server-side state that a session id
would protect, and stateless removes a failure mode where a container
reconnecting after a restart lands on an expired session.

`transport_security` is the SDK's Host-header validation, which exists to stop
DNS rebinding. It is not access control and is not claimed as any. Binding to a
gateway address requires the gateway host be allowlisted or every request is
rejected, so this setting is load-bearing for the feature to work at all.

### Ports do not collide

Each saddle session gets its own network and therefore its own gateway
address. `gw1:9100` and `gw2:9100` are different sockets. A fixed default port
is safe across parallel sessions, and no ephemeral-port negotiation between
saddle and remem is needed.

## saddle: spawning host services

### Profile

`carry_in.mcp.<name>` gains `spawn`, a command as a list of arguments:

```yaml
carry_in:
  mcp:
    remem:
      spawn: ["remem", "serve", "--http",
              "--host", "{{gateway}}", "--port", "9100",
              "--project", "{{repo}}"]
      url: http://{{gateway}}:9100/mcp
```

`spawn` is optional. A profile that gives only `url` behaves exactly as it does
today: saddle assumes something is already listening.

`profile.Expand` currently rewrites `MCP[].URL` only. It also rewrites each
element of `Spawn`. A third variable joins `{{gateway}}` and `{{proxy_port}}`:
`{{repo}}`, the base name of the repository path the session was opened on
(`filepath.Base(o.Repo)`, the same input `SessionName` already derives from),
which is what remem needs for `--project`.

That value agrees with what remem would resolve on its own. `resolve_project`
resolves a worktree to the main repository's name, so a host session working in
`~/llmworkspace/saddle` and a contained session working in a saddle worktree of
it both file under `saddle`.

### Lifecycle

In `session.Up`, between the existing steps 5 and 6 - after the network exists,
so the gateway is known, and before the container starts:

1. For each carried-in server with a `spawn`, expand its arguments and start
   the command as a child process.
2. Wait for the address in its `url` to accept a TCP connection.
3. If it does not within the readiness timeout, fail `up`.

Teardown kills every child. The children join the resource-acquisition ladder
that `Up` already uses, so a failure at any later step reaps the ones already
started, and `State` carries their pids so `down` can reap them too.

### Readiness is not optional

`px.Listen` is synchronous: the egress proxy is bound before anything else
happens, so nothing races it. A child process is not. Start the container
immediately after `exec` and Claude's MCP client may connect before the server
has bound, which surfaces to the agent as a server that does not exist.

So saddle polls and fails `up` on timeout, rather than starting a session that
looks contained and memory-backed but is only the first of those. This is the
same instinct as `macos.CheckFloor` refusing to run below the verified OS
version: a containment or capability claim that has not been checked is not
made.

A missing `remem` binary fails the same way, at the same point, loudly.

## Trust

`spawn` runs an arbitrary host command, outside the container, as the user.

This is not a new trust boundary. A profile already chooses the container
image and the bind mounts, both of which are at least as consequential, and
profiles are user-authored files on the user's own machine. But saddle is a
security tool, a reader auditing it will ask, and an unwritten answer is
indistinguishable from an unconsidered one.

The endpoint itself is unauthenticated. The boundary is the per-session
`--internal` network: it has no internet route, and sessions are isolated from
each other. Anything with a route to that gateway can already reach the egress
proxy; the memory server is not a weaker link than what is already there.

The accepted risk recorded in the saddle design stands and now has teeth: a
session running with permissions off, reading potentially untrusted repository
or web content, can write to the permanent knowledge store, including
`supersede`. Withholding write tools was considered and rejected - the point of
carrying remem in is that contained work is recorded like any other work, and a
read-only memory would not be worth the transport. A `tools:` scope remains
unimplemented and remains rejected at parse time rather than silently ignored.

## Nothing may silently do nothing

`profile.Parse` refuses `carry_in.ssh_agent` and `carry_in.mcp.<name>.tools`
because a security-relevant field that parses and does nothing is worse than a
field that does not exist. `ssh_agent: true` looks like a forwarded socket;
`tools: recall` looks like a restricted server.

`spawn` is held to that rule. It ships fully wired or not at all: the argument
list is really expanded, the process is really started, readiness is really
waited on, and teardown really kills it. Accepting the key and ignoring part of
it is not an acceptable intermediate state.

## Changes to the saddle design

**Reversed.** `2026-09-05-saddle-design.md` rules that "saddle does not start
that server and cannot know its port," and on that basis that a carried-in
server's port is written literally by the profile author. saddle now can start
it. The port stays literal in the profile, but for a different reason: distinct
gateway addresses make a fixed port safe, so negotiating one would be
machinery bought for nothing.

**Corrected.** That document describes remem's store as SQLite in
`~/Library/Application Support/remem`, and argues against mounting the store
partly to avoid "a host process and a container process racing" as writers.
remem is on Postgres - `session.py` connects with psycopg to a configured DSN.
The conclusion is unchanged and in fact easier: the container never gets file
access to the store, and Postgres handles concurrent writers as a matter of
course.

## Testing

remem, test-first against the existing `tests/test_mcp_server.py` fixtures:

- `--project` pins attribution regardless of the process's working directory.
- An explicit `project` argument still overrides the pin.
- HTTP mode yields a null session id even with `CLAUDE_SESSION_ID` set.
- The stdio path is unchanged: no pin, cwd-derived project, env session id.
- The listener is configured with the expected `allowed_hosts` and
  `stateless_http`, asserted without binding a socket.
- CLI flags map onto those parameters, and bare `serve` still runs stdio.

saddle:

- A profile with `spawn` parses; expansion rewrites both `url` and every
  `spawn` element, including `{{repo}}`.
- A profile with `url` and no `spawn` starts nothing.
- `up` fails when the child never binds, within the timeout.
- `up` fails when the command does not exist.
- Teardown kills the child, on both the success path and a mid-ladder failure.

saddle's tests use a trivial fake command that binds a port, never a real
`remem`. The two repositories stay independently testable.

## What this does not fix

**Automatic event recording in containers.** `remem install` puts three things
into an agent: an MCP server, a hook, and a skill. This design delivers the
server. The skill is files and already mounts. The hook shells out to the
`remem` binary, which does not exist inside a Linux container. Contained
sessions get the seven tools and no automatic recording. Unchanged, still
deferred.

**Attach after `up` exits.** The known gotcha - `saddle attach` on a session
whose original `up` process has gone points `HTTPS_PROXY` at nothing - now
applies to the memory server too, for the same reason. The symptoms differ:
egress fails closed, so the session looks inexplicably offline, while a dead
remem endpoint makes tool calls error visibly. Both are fixed by the same work,
persisting and rebinding host-side services, which belongs with parallel
sessions.

## Open risks

- The readiness timeout is a guess until it meets a cold `remem` start with
  migrations to run against Postgres. If it proves tight, it is a constant, and
  the failure is loud rather than silent.
- Killing a child process on teardown is best-effort. A wedged server that
  ignores termination leaves a bound socket on a gateway whose network saddle
  is about to delete; the socket goes with the network, but the process may
  linger. Worth checking during manual verification.
