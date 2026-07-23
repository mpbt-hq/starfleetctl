# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright © 2026 Enrico Weigelt, metux IT consult

GO ?= go
TOOL := starfleetctl

.PHONY: all build test fmt vet clean

all: clean build vet fmt test

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
