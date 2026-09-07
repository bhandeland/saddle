# Quality gate.
#
#   make check   fmt -> lint -> test. The verification command for code changes.
#   make image   builds the session image. Slow, and deliberately NOT part of
#                check: the inner loop must never wait on an image build.
#
# Tools are pinned and installed into ./bin on first use. Bumping a version
# here reinstalls; nothing is taken from the ambient PATH.

GOLANGCI_VERSION  := v2.13.2
GOTESTSUM_VERSION := v1.13.0

BIN       := $(CURDIR)/bin
GOLANGCI  := $(BIN)/golangci-lint-$(GOLANGCI_VERSION)
GOTESTSUM := $(BIN)/gotestsum-$(GOTESTSUM_VERSION)

# check's stages run in order and stop at the first failure, which -j defeats.
.NOTPARALLEL:
.PHONY: check fmt lint test image tools

check: fmt lint test

# Formatting is applied, never reported: gofumpt and goimports rewrite the
# files, so there is no diff for anyone to read and nothing to hand-fix.
fmt: $(GOLANGCI)
	@$(GOLANGCI) fmt

lint: $(GOLANGCI)
	@$(GOLANGCI) run

# One line per package, failures expanded, packages without tests hidden.
# pkgname beats dots-v2 here on the metric that matters: fewer bytes (356 vs
# 531), and no terminal-width warning when stdout is not a tty, which is the
# case whenever an agent captures this.
#
# No -v. No -count=1 either: a cached pass is free and silent, and re-running
# it is pure cost. -race and coverage belong in CI, not in this loop.
test: $(GOTESTSUM)
	@$(GOTESTSUM) --format=pkgname --format-hide-empty-pkg -- -failfast ./...

tools: $(GOLANGCI) $(GOTESTSUM)

$(GOLANGCI):
	@rm -f $(BIN)/golangci-lint*
	@GOBIN=$(BIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	@mv $(BIN)/golangci-lint $@

$(GOTESTSUM):
	@rm -f $(BIN)/gotestsum*
	@GOBIN=$(BIN) go install gotest.tools/gotestsum@$(GOTESTSUM_VERSION)
	@mv $(BIN)/gotestsum $@

# The session image: the environment Claude Code runs in inside a container,
# not a build of saddle, which runs on the host. Out of `check` on purpose -
# it is minutes, not milliseconds, and it only changes when the Dockerfile
# does. -q keeps a successful build to a single line; a failure still prints.
#
# A cold build is minutes of total silence under -q, which is hard to tell
# from a hang, so BUILD_QUIET is overridable: `make image BUILD_QUIET=`.
IMAGE ?= saddle-verify:local
BUILD_QUIET ?= --quiet

image:
	@container build $(BUILD_QUIET) --tag $(IMAGE) --file Dockerfile .
