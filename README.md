# saddle

Run Claude Code in a contained environment on macOS: a git worktree it can
write, a toolchain it cannot pollute, and a network it cannot escape.

## Requirements

- macOS 26 (Tahoe) or newer
- Apple `container` 1.3.1 or newer (`brew install container`)
- A session image containing the `claude` CLI (see below)

## Install

Build from source:

    go build -o saddle ./cmd/saddle

## Build a session image

saddle runs `claude` **inside** the container, and does not carry the host's
copy in. So the image a profile names must already contain the Claude Code
CLI; a bare `golang` or `node` image will start and then fail to run anything.
The included Dockerfile builds one - Go, git and `claude` - and the build is
deliberately not part of `make check`:

    make image     # tags saddle-verify:local

Any image works as long as `claude`, `git` and whatever toolchain the profile
implies are on its PATH.

A Homebrew tap is planned but **not yet published**, so this does not work
yet:

    brew install nighthawk-oss/saddle/saddle   # not available yet

## Create a profile

saddle ships no profiles. Without one, `saddle up` stops at `no profile
matched`. Profiles live in `~/.config/saddle/profiles`, one YAML document
per file:

    mkdir -p ~/.config/saddle/profiles
    cat > ~/.config/saddle/profiles/go.yaml <<'YAML'
    name: go
    image: saddle-verify:local
    detect: [go.mod]
    resources:
      cpus: 4
      memory: 8g
    egress:
      allow:
        - api.anthropic.com
        - platform.claude.com
        - proxy.golang.org
        - sum.golang.org
    carry_in:
      skills: [bag]
      mcp:
        saddlebag:
          spawn: ["bag", "serve", "--http",
                  "--host", "{{gateway}}", "--port", "9100",
                  "--project", "{{repo}}"]
          url: http://{{gateway}}:9100/mcp
    YAML

The image must contain `claude`; `saddle-verify:local` above is the one
`make image` builds. `detect` lists filenames; a profile matches when any of them is present at
the top of the repository, which is what lets `saddle up` choose without
`--profile`. `egress.allow` is the whole allowlist - matching is exact and
case-insensitive on the hostname, with no wildcards, and everything else is
denied. `carry_in.skills` names directories under `~/.claude/skills`, which
are mounted read-only - a plugin that installs skills elsewhere cannot be
carried in this way. String values may use `{{gateway}}`, `{{proxy_port}}`,
and `{{repo}}`, which are substituted once the session's network exists.
`{{repo}}` is the name of the repository the session was opened on, resolved
the way git resolves it - a subdirectory or a linked worktree both name the
main repository - so a memory server carried in files its writes under the
same project the host would.

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

Every session also runs a second, idle container called `<session>-anchor`,
using the same image, with 1 cpu and 256m. It exists because Apple `container`
puts a network's gateway address on the host only while a container on that
network is running - and the egress proxy and any carried-in server bind
exactly that address. The anchor holds it open, which is also what lets
`saddle attach` bring a session's egress and memory server back after the
original `saddle up` has exited. `saddle down` removes it; it never appears
in `saddle ls`.

The anchor puts one requirement on a profile's image: it must have `sleep`.
Any image with `claude`, `git` and a shell does; a distroless one would not.

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
