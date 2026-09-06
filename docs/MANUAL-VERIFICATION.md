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
- [ ] Egress, *no second route out*: the check above only exercises the
      proxy, and would still pass if the container had another way to the
      internet. So verify the containment claim directly: inside the
      session, unset `HTTPS_PROXY` and `HTTP_PROXY` and confirm a direct
      connection to a public address fails - by IP as well as by name, e.g.
      `curl --max-time 10 https://1.1.1.1/` and
      `curl --max-time 10 https://example.com/`. Both must fail. A success
      means the isolation is not holding and no allowlist is meaningful.
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
