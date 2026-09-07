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

## There is no `make image`

saddle does not build a container image. It runs a **prebuilt upstream image**,
named by the `image:` field of a profile and handed to the Apple `container`
CLI (`internal/profile`, `internal/runtime/applecontainer.go`). There is no
Dockerfile in this repository, so there is no container build to run, nothing
for hadolint to lint, and no locally-built image to scan.

If a Dockerfile is ever added, the container build belongs in a separate
`make image` target and must stay out of `make check`: the inner loop should
never wait on an image build.

## Manual verification

Container behaviour that no test covers is tracked in
`docs/MANUAL-VERIFICATION.md`. `make check` does not touch it.
