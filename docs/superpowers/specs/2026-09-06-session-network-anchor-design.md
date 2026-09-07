# Session network anchor

**Status:** approved, not implemented
**Date:** 2026-09-06

## Problem

`saddle up` cannot start a contained session. It fails at step 5:

    saddle: bind egress proxy on 192.168.128.1: listen tcp 192.168.128.1:0:
            bind: can't assign requested address

`runtime.CreateNetwork` documents the assumption this rests on - "When
isolated is true the network has no route to the internet, but the host
remains reachable at the gateway." That is false on Apple `container` 1.3.1.

## Evidence

Measured directly, one network at a time, on macOS 26 with container 1.3.1.
`container network inspect` reports an `ipv4Gateway` from the moment the
network is created; the question is whether that address is on a host
interface, which is what a host-side listener needs.

| point in the lifecycle              | host can bind the gateway |
| ----------------------------------- | ------------------------- |
| after `container network create`     | no - EADDRNOTAVAIL        |
| after `container create` (not started) | no                      |
| after `container start`              | **yes**                   |
| after the container **stops**        | no                        |
| after the container is removed       | no                        |

Two facts follow, and the design turns on both:

1. The gateway address is materialised by a **running** container, not by the
   network.
2. It is **refcounted**. When the last running container on the network stops,
   the address leaves the host again.

`--internal` and normal networks behave identically. Only the `default`
network's `192.168.64.1` exists ahead of time, which is why nothing caught
this: every existing test either uses a fake runtime or, in the contract
tests, a network whose gateway happens to already be up.

## Why this breaks the current ordering

`Up` runs: create network (4), bind the egress proxy on the gateway (5),
expand `{{gateway}}` and `{{proxy_port}}` (6), start spawned host services
(7), create the session container (9), attach (11). `runtime.Create` makes the
container **without starting it**; the start happens inside `AttachArgv`,
which is `container start -ai`.

So the address the proxy needs at step 5 cannot exist until step 11. Every
contained `saddle up` fails, always, on a freshly created network.

Note that the egress proxy is not the only host-side listener affected: a
profile's `carry_in.mcp.<name>.spawn` binds the gateway too - the shipped
example is `remem serve --http --host {{gateway}} --port 9100` - so it would
fail at step 7 for the same reason.

Fact 2 explains a second, previously separate symptom, recorded as a gotcha
after the remem HTTP work: `saddle attach` after the original `up` exits
leaves the memory server dead as well as the proxy. That is not a process
lifetime bug. When claude exits, the session container stops, the gateway
address leaves the host, and nothing can bind it again.

## Ruled out

**Start the session container detached, then bind.** Apple `container` has no
`attach` subcommand; `container start -ai` *is* the attach. Once PID 1 is
running detached there is no way to put a TTY on it.

**Bind the proxy on 0.0.0.0, or on the `default` gateway.** 0.0.0.0 exposes
the proxy on every host interface, which `saddle doctor` already warns about
as a hazard. The `default` gateway always exists but is unreachable from an
`--internal` session network, which is the point of using one.

**The session container anchors itself** - PID 1 becomes a no-op and claude
runs under `container exec -it`. This works and needs no second container, and
it makes `saddle attach` a genuine exec rather than a restart. It was not
chosen: "container running" would stop meaning "session active", which moves
`waitForExit`, `ls` and `down` all at once, on the code path where the last
review already found two ordering-and-teardown bugs. It also leaves the full
session VM running after claude exits, where the chosen design leaves only a
64m anchor. Worth revisiting if attach/detach becomes a routine workflow
rather than a recovery path.

## Design

### The anchor

Every session owns a second container, `<session>-anchor`, whose only purpose
is to hold the gateway address on the host. It runs the **profile's own
image** - guaranteed present, since the session needs it anyway, so no second
pull and nothing extra to keep current - with `sleep infinity`, 1 cpu, 64m, on
the session network. No mounts, no environment, no token. Nothing runs in it.
It is a refcount held open.

The anchor is created for **every** session, including `--open-net` ones that
start no proxy at all. A profile's spawned host services bind the gateway too,
so the address has to be up whether or not saddle itself is listening on it.

The anchor is not a session and never appears as one: `saddle ls` lists state
files, and `Reconcile` matches on the recorded container id, so an extra
container on the network is invisible to both.

This puts one requirement on a profile's image: it must have `sleep`. Any
image carrying `claude`, `git` and a shell does; a distroless one would not.
The README's profile section says so.

`runtime` grows `Start(ctx, id)` for a detached start (`container start`,
without `-ai`), distinct from `AttachArgv`.

### Ordering in Up

     4.  create network
     4b. create + start anchor                        (new)
     4c. wait until the gateway is bindable           (new)
     5.  bind egress proxy on gateway:0               (unchanged)
     6.  expand {{gateway}} / {{proxy_port}} / {{repo}} (unchanged)
     7.  start spawned host services                  (unchanged)
     8-9. mcp.json, token, create session container   (unchanged)
    10.  persist state, now carrying Anchor
    11.  attach                                       (unchanged)

Step 4c polls with a real probe bind on the gateway until it succeeds, with a
timeout and an error that names the address. It does not sleep a fixed
interval: the address appeared a beat *after* `container start` returned in
every measurement, so the delay is real, but its length is not a constant
worth guessing.

Everything downstream of step 5 is untouched. The ephemeral `:0` bind still
works, because the gateway now exists before the session container is created,
and `{{proxy_port}}` is still known by the time placeholders are expanded.

### Teardown

`State` gains `Anchor`.

`Down` tears down in order: session container, anchor, network. The network
cannot go first, and the anchor cannot go before the container sharing its
network.

`Up`'s unwind removes the anchor on any failure after 4b. Anchor removal
failures **warn and continue** - they never abort the rest of the teardown.
The previous review found a Critical of exactly this shape, where a failure
mid-teardown left a session that could only be removed with `--force`, the one
flag that also bypasses the uncommitted-changes guard.

### saddle attach

`attach` loads the state file, ensures the anchor is running - starting it if
stopped, failing clearly if it has been removed - and then binds the proxy at
**exactly** `st.ProxyAddr`. Not a fresh ephemeral port: the container's
`HTTPS_PROXY` was baked in at create time and still points at the old one. It
then starts the spawned host services and runs `container start -ai` as it
does today, killing what it started when it exits.

If that port is already bound, `attach` refuses and names the conflict. Two
processes attached to one session is not supported, and silently sharing a
proxy would be a worse outcome than being told.

Ownership stays where it is today: whichever saddle process is attached owns
the host-side services for its own lifetime. While nobody is attached the
session container is stopped, so nothing inside needs egress. A persistent
per-session supervisor was considered and rejected as machinery this does not
yet need.

## Testing

**Unit, over the fake runtime.** Ordering: network, then anchor start, then
proxy bind, then session container create. The unwind removes the anchor.
`Down`'s teardown order. `State` round-trips `Anchor`.

**Contract (`-tags contract`, real `container` CLI).** The gateway lifecycle
in the evidence table, asserted rather than assumed: not bindable after
`network create`, not bindable after `container create`, bindable after
`container start`, and not bindable again after the container stops. The last
row is the one the whole design rests on.

That contract test is the direct answer to why the suite missed this. Nothing
exercised a freshly created network against a real host bind.

**Manual.** `docs/MANUAL-VERIFICATION.md` end to end, starting with the two
items the remem HTTP review flagged as carrying the weight: no second route
out, and a no-explicit-project write filing under the repository's name.

## Out of scope

- Parallel sessions, and persisting host-side services across them.
- Detecting an anchor that dies mid-session. The session loses egress and the
  operator sees connection failures; no health check is proposed.
- The `saddle: EOF` message when `saddle auth` runs without a TTY.
