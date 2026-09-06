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
- [ ] `saddle down <name>` on a session with uncommitted changes in its
      worktree: confirm it refuses and explains why, and that `--force`
      then proceeds and tears everything down (container, network, state
      file, worktree).
- [ ] `saddle down <name>` on a clean session: confirm it tears down
      without needing `--force`.
- [ ] `saddle ls` after the above: confirm no stale entries remain.
