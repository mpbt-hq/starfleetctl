#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright © 2026 Enrico Weigelt, metux IT consult
#
# Run unit tests for the opencode plugin TypeScript.
# Uses tsx to run tests directly.

set -euo pipefail
cd "$(dirname "$0")/.."

PLUGIN_TEST="fragments/opencode-plugins/starfleet-dispatch.test.ts"

note() { printf 'test-opencode-plugin: %s\n' "$*"; }

if [ ! -f "$PLUGIN_TEST" ]; then
    note "test file not found: $PLUGIN_TEST"
    exit 1
fi

# Check if tsx is available
if ! command -v npx >/dev/null 2>&1; then
    note "npx not found — skipping tests (install node/npm)"
    exit 0
fi

# Try to run tests with tsx (installed as dev dependency)
if npx --no-install tsx --version >/dev/null 2>&1; then
    note "Running plugin unit tests with tsx..."
    if npx --no-install tsx "$PLUGIN_TEST"; then
        note "Plugin unit tests PASSED"
        exit 0
    else
        note "Plugin unit tests FAILED"
        exit 1
    fi
else
    note "tsx not installed — skipping tests (run 'npm install' in starfleetctl dir)"
    exit 0
fi
