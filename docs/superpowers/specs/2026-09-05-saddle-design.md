# saddle: isolated Claude Code sessions on macOS

Design, 2026-09-05.

## Problem

Running Claude Code with permissions disabled is the only way to get unattended
work out of it, and on macOS there is nothing that makes that safe. Incus gives
Linux users cheap, disposable, network-constrained instances. macOS has no
equivalent. Apple's `container` (1.3.1, in homebrew-core) is close enough to
build one.

`saddle` launches Claude Code in a contained environment: a git worktree it can
write, a toolchain it cannot pollute, a network it cannot escape, and the parts
of Brandon's Claude setup that are deliberately carried in.

## Goals

Four, in the order they will be built:

1. **Blast-radius containment.** Permissions off, but the agent reaches only
   the worktree it was given.
2. **Reproducible per-project toolchains.** No dependence on host Homebrew.
3. **Network egress control.** Deny by default, allowlist per profile.
4. **Parallel disposable sessions.** N at once, no collisions.

**First slice: one session, contained, real work.** `saddle up <repo>` mounts a
worktree, runs Claude with permissions off, and you can finish a task in it and
get a branch back.

The first slice covers goals 1-3. Only parallelism (4) is deferred, and the
design must not preclude it.

This is a deliberate change from the original scoping, which put egress after
the first slice. Two things moved it forward. Permissions-off is the default,
and that default is defensible only because of the containment, so shipping an
agent with permissions disabled and unrestricted network access would be the
weakest available combination. And the spike showed egress costs one network
flag plus a small host proxy, not a subsystem.

## Non-goals

- A plugin system. Backends are not loadable at runtime and there is no driver
  ABI. See "Runtime boundary".
- Multi-user or remote hosts. Single machine, single user.
- Replacing cmux. saddle renders into cmux; it does not depend on it.
- Isolating the container from host services. See "Known limitations".

## Spike findings

Apple `container` 1.3.1 was installed and probed on macOS 26.6.2 (arm64) on
2026-09-05. All four questions were answered with evidence.

| Question | Result |
|---|---|
| Bind-mount a host worktree, writes land back correctly? | **Yes.** Container writes appear on the host owned by uid 501. Caveat: gid flattens to `0`, not `20`. |
| Cold start cost per session? | **0.61s warm**, ~7s on the very first run (init image unpack). |
| Egress filtering possible? | **Yes**, via `--internal` networks. See below. |
| Claude Code on linux/arm64 with an injected token? | **Yes.** `npm i -g @anthropic-ai/claude-code` installs clean, runs v2.1.261, reports "Not logged in" absent credentials. |

Two egress mechanisms exist and they are not equal.

`--cap-add NET_ADMIN` works: iptables rules apply inside the container. It is
the weaker option, because an agent running as root with `NET_ADMIN` can flush
the rules it is meant to be bound by. Making it safe requires an entrypoint that
installs rules and then drops privileges.

`container network create --internal` is the mechanism we use. Verified: a
container on an internal network has **no route to the internet**, and the
**host remains reachable at the network gateway** (192.168.128.1). Deny by
default plus a host-side allowlisting proxy is therefore enforceable without any
in-container cooperation. There is no route for the agent to tamper with.

Also noted and not probed: `--publish-socket` (container socket to host) and
`--ssh` (SSH agent forwarding).

## Architecture

Seven units. Exactly one of them knows what a container is.

```
saddle up <repo> [--profile P]
        |
  +-----v------+
  |  session   |  orchestrates; owns state dir + lifecycle
  +--+---+---+-+
     |   |   +---------------------------+
     v   v                               v
 +--------+ +----------+ +---------+ +----------+
 |profile | | worktree | | egress  | |  render  |
 |resolve | |  (git)   | | (proxy) | |term/cmux |
 +--------+ +----------+ +----+----+ +----------+
                              | HTTPS_PROXY
                       +------v------------+
                       |  runtime package  |  <- the boundary
                       |  (applecontainer) |
                       +-------------------+
```

**`session`** owns the lifecycle and a state directory at
`~/.local/state/saddle/sessions/<name>/session.json`: profile, worktree path,
container id, proxy port, status. It is the only unit that coordinates.
Recovery after a crash is reading those files back, which is also what makes
`saddle ls` and `saddle down` honest rather than a guess.

**`profile`** resolves a named or detected profile into a concrete session plan.
Pure function: config in, plan out, no I/O beyond reading config. Testable with
no container present.

**`worktree`** creates and removes host git worktrees. Knows nothing about
containers.

**`egress`** is the host-side allowlisting CONNECT proxy, bound to the gateway
address the runtime reports. Backend-agnostic by construction.

**`render`** attaches you to a running session: inherit stdio, or hand the
attach argv to cmux. tmux slots in here later as a third implementation.

**`runtime`** is the boundary. See below.

The invariant worth defending: `egress` and `render` never import `runtime`.
They consume values from the session plan (a gateway address, an argv). If
either starts needing runtime-specific knowledge, the boundary is wrong.

## The outside: CLI surface and semantics

```
saddle up [path]            create a session and attach
saddle ls                   list sessions
saddle attach <name>        re-attach to a session
saddle down <name|--all>    tear down
saddle exec <name> -- cmd   run a command inside a session
saddle profile ls|show      inspect profiles
saddle auth                 store the container's Claude token
saddle doctor               preflight everything
```

```
$ saddle up ~/llmworkspace/saddle
  session   saddle-fix-auth
  profile   go (detected)
  worktree  ~/.local/state/saddle/wt/saddle-fix-auth  [branch saddle/fix-auth]
  egress    api.anthropic.com, proxy.golang.org, +2      (all else denied)
  attach    cmux workspace 3
```

Five semantics that are expensive to change later:

**Permissions are off by default.** `--dangerously-skip-permissions` is the
point; if you still approved every edit the container bought nothing. `--safe`
restores prompting. This is only defensible because of the containment, which
makes every containment weakness a correctness bug.

**Containers are cattle, worktrees are pets.** On exit the container is removed
and the worktree stays. Value lives in commits on `saddle/<name>`, and because
the worktree is a host bind-mount there is no export step. `saddle down` removes
the worktree and refuses if it is dirty without `--force`. Refusing on unpushed
commits is **not implemented**; only the dirty check exists. `git worktree
remove` leaves the `saddle/<name>` branch and every commit on it intact, so a
teardown loses the working directory, never committed work.

**Sessions are named from repo and branch**, collision-suffixed, overridable
with `--name`. Unpronounceable ids make `saddle ls` useless.

**State is files, not memory.** `saddle ls` reconciles the state directory
against `container list`, so a crashed saddle or a killed container produces
accurate output.

**Egress is per-profile with per-session overrides.** `--allow <host>` adds one,
`--no-net` denies everything, `--open-net` disables the proxy and warns loudly.
Default is deny.

`saddle auth` stores an OAuth token in a saddle-owned Keychain item, separate
from Brandon's Claude login credentials, injected as `CLAUDE_CODE_OAUTH_TOKEN`.
The host keychain is unreachable from a Linux container, so this is required,
not optional. Accepted risk: that token lives in a container running an agent
with permissions off. The containment protects the filesystem and the network;
it does not protect the token from the agent.

`saddle doctor` exists because setup genuinely cost three separate confusing
failures during the spike: `container` installed but no kernel, service not
started, no token. Doctor turns those into one checklist with fixes.

## Profiles and carry-in

```yaml
name: go
image: ghcr.io/brandon/saddle-go:1.24
detect: [go.mod]
resources: {cpus: 4, memory: 8g}

egress:
  allow: [api.anthropic.com, proxy.golang.org, sum.golang.org]

carry_in:
  skills: [remem, superpowers]
  mcp:
    remem:
      spawn: ["remem", "serve", "--http",
              "--host", "{{gateway}}", "--port", "9100",
              "--project", "{{repo}}"]
      url: http://{{gateway}}:9100/mcp
```

Two fields deliberately do **not** appear above. `carry_in.ssh_agent` and a
per-server `tools:` scope are both unimplemented, and a security-relevant
field that silently does nothing is worse than one that does not exist, so
saddle rejects a profile that sets either.

Skills carry in as a read-only mount, because they are files. MCP servers carry
in as an endpoint URL. Everything else stays out unless a profile names it.
`detect` is what lets `saddle up` pick a profile without a flag.

Profile string values support substitution of session-scoped values that are
not known until the container exists: `{{gateway}}` is the host address the
container can reach, and `{{proxy_port}}` is the port the egress proxy bound
to. Those are the only two. A carried-in MCP server's port is written literally
by the profile author. saddle can now start such a server - see
`carry_in.mcp.<name>.spawn` and `2026-09-06-remem-http-transport-design.md` -
but a fixed port needs no negotiating, because each session's gateway is a
distinct address.

### remem

remem is in the default profile, **read-write**. Contained sessions record work
like any other session.

remem is a macOS arm64 binary, so it cannot itself be mounted into a Linux
container. `remem serve` was **stdio only** as of 2026-09-05. remem uses the
official `mcp` Python SDK 2.1.1, which already ships streamable-HTTP transport,
so serving it over a port was a transport swap rather than a rewrite.

**Resolved.** That change landed in remem, and saddle carries it in over HTTP -
see `carry_in.mcp.<name>.spawn` above and
`2026-09-06-remem-http-transport-design.md`.

remem's store is Postgres (`session.py` connects with psycopg to a configured
DSN), reachable from the host but not from the container. The container never
gets file access to the knowledge base, and concurrent writers are Postgres's
ordinary business.

Accepted risk: a session running with permissions off, reading potentially
untrusted repo or web content, can write to the permanent knowledge store. The
bridge remains the policy point, so `tools: recall` could be offered
per-profile for a hardened profile later. It is not implemented today, and a
profile that sets it is rejected rather than silently carrying a read-write
server.

**Known regression.** `remem install` puts three things into an agent: the MCP
server, a hook, and a skill. The endpoint carries the server and the skill is
files, but the hook shells out to the `remem` binary, which does not exist in
the container. In-container sessions therefore get remem's tools but **not
automatic event recording**. Deferred deliberately; revisit once saddle is in
real use.

## Egress

Container is placed on a per-session `--internal` network: no internet, host
reachable at the gateway. saddle runs a host-side CONNECT proxy bound to that
gateway and injects `HTTPS_PROXY` / `HTTP_PROXY`. The proxy enforces the
profile's domain allowlist. Off-allowlist hosts are refused at the proxy;
everything else has no route at all.

The agent cannot bypass this. It has no route out to tamper with, unlike
in-container iptables rules a root agent with `NET_ADMIN` could flush.

## Runtime boundary

There is **no `Runtime` interface in the first slice.** One package owns
everything that knows Apple `container` exists, and nothing else imports it.
That is a package boundary and a review rule, not a framework.

When a second backend is real, the interface is **extracted from two
implementations rather than predicted from one**. A paper translation of one
real session to rootless podman was done during design and produced four
findings, which are already banked as constraints on the outside:

1. **Egress must be expressed as a property, never a mechanism.** Apple's
   `--internal` leaves the gateway reachable. Podman's `--internal` is believed
   to drop the gateway too, which would break the contract outright. A
   `UseInternalNetwork bool` would have been unimplementable on Linux.
2. **Gateway addresses are discovered, not computed.** Apple's is the subnet's
   `.1`; podman's requires inspecting the network.
3. **uid correctness is a promise, not a parameter.** Rootless podman needs
   `--userns=keep-id`; Apple `container` needs nothing. A `UserNS` field would
   be meaningless on macOS.
4. **Mount options are driver-owned.** Linux may need SELinux relabeling (`:Z`),
   which has no macOS analogue.

Cost accepted: Incus's native `network acl` is a better egress mechanism than
our proxy, and nothing here lets a backend say so. The proxy works everywhere;
a future backend can advertise native egress without disturbing anything above.

## Testing

Most of saddle is testable with no container running.

- `profile` - pure function, table tests.
- `worktree` - real git in temp dirs, hermetic.
- `egress` - allowlist logic via `httptest`.
- `session` - fake runtime; the case that matters is state reconciliation when
  a container died behind saddle's back.
- `doctor` - one test per failure mode hit during the spike.

**Contract tests** run against a real backend and are the acceptance suite a
future podman driver must pass:

- *uid contract*: write from inside the container, assert it appears on the host
  owned by the invoking user.
- *network contract*: off-allowlist host unreachable, on-allowlist host
  reachable, gateway reachable.

One end-to-end test: `saddle up` a fixture repo, run a trivial command, assert a
commit appears on the host, tear down.

## Release

- **Apache-2.0** from the first commit. DFSG compatibility is a hard gate for
  homebrew-core later and relicensing after contributors is painful.
- **goreleaser**, tagged releases, SHA-256, from day one.
- **Personal tap first**: `brandon/homebrew-saddle`. No notability bar.
  Formula declares `depends_on "container"` and a macOS floor.
- **macOS floor: 26 (Tahoe).** `container` supports macOS 15, but the entire
  egress design rests on `--internal` behavior verified only on 26.6. Claiming
  support for an OS where containment might silently not hold is worse than
  requiring a newer one.
- **homebrew-core** once self-submission thresholds are met: 90 forks, 90
  watchers, or 225 stars, repo at least 30 days old.

Language: **Go**. saddle is subprocess orchestration, JSON parsing, TTY
plumbing, and one small HTTP proxy; the standard library covers all of it with
no dependencies, and it cross-compiles to a single bottled binary. The CLI-first
design means saddle never needs a PTY library, because `container run -it`
allocates the TTY and saddle inherits stdio. If transcript capture is ever
required, saddle needs a real PTY and that decision should be revisited.

## Known limitations

**The container can reach host services bound to `0.0.0.0`.** Traffic to the
gateway does not traverse the egress proxy, which is what makes the remem
endpoint work. The same is true of every other listener on that interface. This
was observed during the spike: the container could have reached a
`python3 -m http.server` running on the host. saddle blocks the internet except
via the allowlist; it does **not** isolate the container from local dev servers,
databases, or unauthenticated admin UIs. `saddle doctor` warns about listeners
on that interface.

**gid flattens to 0** on mounted files rather than preserving the host's `20`.
Harmless for git worktrees; noted in case it bites.

**The OAuth token is reachable by the agent.** See "The outside".

**No automatic remem recording in contained sessions.** See "remem".

**DNS resolution is not pinned.** The egress proxy allowlists by hostname: it
checks the `CONNECT` target's name and then dials that name. It does not pin
the address the name resolved to during the check, so a hostname that
re-resolves to a different address between the check and the connection is not
caught. An attacker who controls DNS for an allowlisted name, or who can win
that race, reaches an address of their choosing through the proxy.

### Verified containment properties

Verified by test on 2026-09-05 against Apple `container` 1.3.1 on macOS 26.6.
The scope is that runtime version only; neither property is a guarantee for
other versions or backends.

- **Per-session networks are isolated from each other.** A container on
  session A's network can reach its own gateway but **not** another session's
  gateway. One session therefore cannot borrow another session's egress proxy
  to reach hosts its own allowlist denies.
- **An `--internal` network provides no working DNS.** `/etc/resolv.conf`
  inside the container points at the gateway, but the resolver there refuses
  connections. There is no unfiltered outbound DNS channel to tunnel over or
  exfiltrate through.

## Open risks

1. **Podman `--internal` gateway behavior is unverified.** Finding 1 above is an
   assumption. Cheap to settle before the Linux driver is built; it does not
   affect the first slice.
2. **Apple `container` on GitHub hosted macOS runners is unverified.** Runners
   are VMs and per-container VMs need nested virtualization that may not be
   exposed. If unavailable, unit tests run in CI and contract tests run on a
   self-hosted runner. Verify early, since it shapes how much a green PR means.
3. **Image supply chain.** Profiles name images; a compromised base image is
   inside the containment, not outside it. Pin by digest.

## Decisions

| Decision | Rationale |
|---|---|
| Apple `container`, not Docker/Lima | Native, no daemon VM to manage, 0.61s warm start, and `--internal` networking gives enforceable egress. |
| Package boundary, not a `Runtime` interface | Extract from two implementations, do not predict from one. |
| Host git worktree, bind-mounted | Work is already out; no export step. Reuses existing worktree workflow. |
| Deny-by-default egress via host proxy | Portable across backends, and unbypassable from inside. |
| Curated per-profile carry-in | Every convenience crossing the boundary erodes the isolation. |
| remem read-write by default | Contained work should be recorded like any other work. |
| CLI-first, cmux as a renderer | cmux is an output target, never a dependency. |
| Go | Subprocess and stdlib work; single bottled binary for Homebrew. |
