# saddle

## Verification

`make check` is the only verification command for code changes. It runs three
stages in order - fmt, lint, test - and stops at the first one that fails.
Do not run `go test`, `go vet` or `golangci-lint` directly; the gate is tuned
to emit one line per diagnostic, and the bare tools are not.

    make check

**Formatting is auto-fixed, never hand-edited.** The fmt stage runs gofumpt and
goimports through golangci-lint and rewrites the files in place. If it changes
something, that is the fix - do not reformat by hand, and do not treat the
rewrite as a finding to act on.

Tools are pinned in the Makefile (`GOLANGCI_VERSION`, `GOTESTSUM_VERSION`) and
installed into `./bin` on first use. golangci-lint's own policy is that a minor
bump can surface new errors, so the pin is what keeps the gate from changing
underneath a branch. Nothing is taken from the ambient PATH.

The test stage deliberately does not pass `-count=1`: a cached pass is free and
silent. `-race` and coverage belong in CI, not in this loop.

## `make image`

`make image` builds the **session image** - the environment Claude Code runs
in inside a container. It is not a build of saddle, which runs on the host.

Run it only when `Dockerfile` or its inputs change. It is deliberately not a
stage of `make check`: it takes minutes, and the inner loop must never wait on
an image build.

    make image     # tags saddle-verify:local; override with IMAGE=...

saddle runs `claude` inside the container (`internal/session/up.go`) and does
not mount the host's copy in, so the image a profile names must contain the
`claude` CLI. An image without it starts and then fails to run anything.

hadolint and trivy are deliberately absent: the Dockerfile is two stages and
tracked, and neither tool is among the three dependencies this project's gate
is allowed.

## Manual verification

Container behaviour that no test covers is tracked in
`docs/MANUAL-VERIFICATION.md`. `make check` does not touch it.
