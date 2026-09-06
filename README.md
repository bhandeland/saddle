# saddle

Run Claude Code in a contained environment on macOS: a git worktree it can
write, a toolchain it cannot pollute, and a network it cannot escape.

## Requirements

- macOS 26 (Tahoe) or newer
- Apple `container` 1.3.1 or newer (`brew install container`)

## Install

    brew install brandon/saddle/saddle

## Quick start

    saddle doctor          # check the setup
    saddle auth            # store a token from `claude setup-token`
    saddle up ~/code/myrepo

## Known limitations

saddle blocks internet egress except through its allowlist proxy. It does
**not** isolate the container from host services bound to `0.0.0.0` -
local dev servers and databases on that interface are reachable from a
session. `saddle doctor` warns about them. The egress proxy also does not
pin DNS resolution, so an allowlisted hostname that re-resolves to a
different address between the allowlist check and the connection is not
caught.

See `docs/MANUAL-VERIFICATION.md` for the checks that require a real token
and live containers.

## Licence

Apache-2.0
