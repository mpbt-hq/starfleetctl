# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright © 2026 Enrico Weigelt, metux IT consult

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

build:
	$(GO) build -o $(TOOL) ./cmd/starfleetctl

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
