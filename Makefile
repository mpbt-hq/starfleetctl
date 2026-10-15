# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright © 2026 Enrico Weigelt, metux IT consult

RELEASE ?= 0.0.3

GO ?= go
TOOL := starfleetctl

# Static build-test for the opencode plugin TypeScript
# (fragments/opencode-plugins/), which the bootstrap deploys as raw,
# un-compiled TS to .opencode/plugins/. Runs an esbuild bundle check
# (syntax + import graph) plus tsc --noEmit (type check) when the toolchain
# is present; both skip gracefully on hosts without node.
# See scripts/check-opencode-plugin.sh.
PLUGIN_CHECK := scripts/check-opencode-plugin.sh

.PHONY: all build test fmt vet clean check-plugin

all: clean build vet fmt test check-plugin

# Go's embedded VCS info (buildvcs) recognises only a .git DIRECTORY, not the
# .git FILE of a linked worktree. Building inside a worktree that sits under
# another git repo (e.g. a starfleetctl worktree under the mpbt-workspace) makes
# buildvcs walk up and stamp the WRONG repo. Pointing GIT_DIR at our own git dir
# makes it stamp the correct commit for clones and worktrees alike, so
# `go version -m <binary>` — including /proc/<pid>/exe of a running daemon —
# reports the version that is actually running.
GIT_DIR := $(shell git rev-parse --absolute-git-dir 2>/dev/null)

build:
	GIT_DIR="$(GIT_DIR)" $(GO) build -o $(TOOL) ./cmd/starfleetctl

test:
	$(GO) test ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

clean:
	rm -f $(TOOL)

check-plugin:
	@$(PLUGIN_CHECK)
