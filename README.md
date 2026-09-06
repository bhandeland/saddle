# saddle

Run Claude Code in a contained environment on macOS: a git worktree it can
write, a toolchain it cannot pollute, and a network it cannot escape.

## Requirements

- macOS 26 (Tahoe) or newer
- Apple `container` 1.3.1 or newer (`brew install container`)

## Install

Build from source:

    go build -o saddle ./cmd/saddle

A Homebrew tap is planned but **not yet published**, so this does not work
yet:

    brew install brandon/saddle/saddle   # not available yet

## Create a profile

saddle ships no profiles. Without one, `saddle up` stops at `no profile
matched`. Profiles live in `~/.config/saddle/profiles`, one YAML document
per file:

    mkdir -p ~/.config/saddle/profiles
    cat > ~/.config/saddle/profiles/go.yaml <<'YAML'
    name: go
    image: docker.io/library/golang:1.24
    detect: [go.mod]
    resources:
      cpus: 4
      memory: 8g
    egress:
      allow:
        - api.anthropic.com
        - proxy.golang.org
        - sum.golang.org
    carry_in:
      skills: [superpowers]
    YAML

`detect` lists filenames; a profile matches when any of them is present at
the top of the repository, which is what lets `saddle up` choose without
`--profile`. `egress.allow` is the whole allowlist - matching is exact and
case-insensitive on the hostname, with no wildcards, and everything else is
denied. `carry_in.skills` names directories under `~/.claude/skills`, which
are mounted read-only. String values may use `{{gateway}}` and
`{{proxy_port}}`, which are substituted once the session's network exists.

## Quick start

    saddle doctor          # check the setup
    saddle auth            # store a token from `claude setup-token`
    saddle up ~/code/myrepo
    saddle ls              # NAME, PROFILE, STATUS, EGRESS, WORKTREE
    saddle attach <name>   # get back into a session
    saddle down <name>     # tear it down

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
