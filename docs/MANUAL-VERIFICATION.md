# Manual verification checklist

The automated suite (`go test ./...` and `go test -tags contract ./...`)
cannot exercise the parts of saddle that need a real Claude OAuth token or
live containers. Before a release, a human must confirm the following by
hand:

- [ ] `saddle auth`: obtain a real token with `claude setup-token`, paste it
      in, and confirm `auth.Load` (or a subsequent `saddle up`) picks it up.
- [ ] `saddle doctor` on a genuinely clean machine (no prior saddle state,
      container service stopped, then started): confirm each check reports
      the right status and that the "fix" text actually resolves the
      failure when followed.
- [ ] `saddle up` on a real repository: confirm a session is created, a
      container starts, and the operator is attached (both `terminal` and
      `cmux` render modes).
- [ ] Inside that session, make a commit. Confirm the commit and the new
      branch are visible on the host in the session's worktree
      (`git -C <worktree> log`), without any extra step to "sync" it.
- [ ] Egress: from inside the session, confirm a request to a
      non-allowlisted host is denied, and a request to a host passed via
      `--allow` (or present in the profile) succeeds.
- [x] Egress, *no second route out*: the check above only exercises the
      proxy, and would still pass if the container had another way to the
      internet. So verify the containment claim directly: inside the
      session, unset `HTTPS_PROXY` and `HTTP_PROXY` and confirm a direct
      connection to a public address fails - by IP as well as by name, e.g.
      `curl --max-time 10 https://1.1.1.1/` and
      `curl --max-time 10 https://example.com/`. Both must fail. A success
      means the isolation is not holding and no allowlist is meaningful.
      Verified 2026-09-08, but from the session's *anchor* container, not the
      session container: `claude` is the session container's entrypoint, so it
      exits the moment it starts (see the TTY blocker). The anchor is on the
      same network, from the same image, with one interface and one default
      route, and carries no proxy vars at all. All six probes failed - by IP
      with a connection timeout, by name with DNS itself timing out, the
      allowlisted host included. Positive control: a host listener on the
      gateway answered 200 from inside the container, so the failures are
      containment and not a dead stack. Redo this from a real session once
      the TTY blocker is gone.
- [ ] `--open-net`: bring a session up with `--open-net` and confirm the
      warning is printed, that unrestricted internet access genuinely works
      from inside it, and that `saddle ls` flags the session's EGRESS
      column conspicuously (`OPEN(!)`) so it cannot be mistaken for a
      contained one.
- [ ] `--no-net`: bring a session up with `--no-net` and confirm all egress
      is denied - through the proxy and directly - and that `saddle ls`
      shows its egress as `none`.
- [ ] `saddle down <name>` on a session with uncommitted changes in its
      worktree: confirm it refuses and explains why, and that `--force`
      then proceeds and tears everything down (container, network, state
      file, worktree).
- [ ] `saddle down <name>` on a clean session: confirm it tears down
      without needing `--force`.
- [ ] `saddle ls` after the above: confirm no stale entries remain.

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

## Carried-in saddlebag over HTTP

- [ ] `saddle up` on a repo whose profile carries saddlebag in. From inside the
      session, call `recall` and confirm results come back.
- [x] From inside the session, write an entry with no explicit `project`.
      On the host, confirm it filed under the repository's name and not under
      the directory `saddle up` was run from. This is the silent failure the
      pinned project exists to prevent, so check it rather than assume it.
- [x] Confirm the entry's session id is null rather than the launching
      shell's.
      Both verified 2026-09-08. The check only discriminates if the server's
      cwd differs from the pinned project - `hostsvc` never sets `c.Dir`, so
      the spawned remem inherits saddle's cwd - so the server was run from
      /tmp. The entry filed under `saddle` with a null session id.
- [ ] `saddle down`, then `ps ax | grep 'bag serve'` - no survivor.
- [ ] Kill the `saddle up` process with SIGKILL rather than exiting cleanly,
      so the child is orphaned. Then `saddle down` and confirm it reaps the
      orphan.
- [ ] Two sessions at once on different repos: confirm both saddlebag servers
      bind :9100 on their own gateways without colliding, and that each files
      entries under its own project.
- [ ] A profile whose `spawn` names a nonexistent binary fails `saddle up`
      with a clear error and leaves no network, worktree, or container behind.
