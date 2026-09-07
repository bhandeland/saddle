# The session image saddle runs. It is not a build of saddle itself - saddle
# runs on the host - it is the environment Claude Code gets inside a session,
# so it needs the `claude` CLI (internal/session/up.go runs it as the
# container's command), git, and the Go toolchain the `go` profile implies.
#
# Nothing is copied from the build context; see .dockerignore.

# Node is lifted from the official slim image rather than installed through
# apt, which on bookworm would pin Node to 18.
FROM docker.io/library/node:22-slim AS node

FROM docker.io/library/golang:1.24
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm

# A cache mount keeps the npm download out of every rebuild.
RUN --mount=type=cache,target=/root/.npm \
    npm install -g @anthropic-ai/claude-code

# Fail the build rather than a session if either tool is missing.
RUN claude --version && git --version && go version
