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

# Prefer a directly installed runner (used by CI), then a local npx runner.
if command -v tsx >/dev/null 2>&1; then
    note "Running plugin unit tests with tsx..."
    tsx "$PLUGIN_TEST"
    note "Plugin unit tests PASSED"
elif command -v npx >/dev/null 2>&1 && npx --no-install tsx --version >/dev/null 2>&1; then
    note "Running plugin unit tests with local tsx..."
    npx --no-install tsx "$PLUGIN_TEST"
    note "Plugin unit tests PASSED"
else
    note "tsx not installed — skipping tests (install tsx to run them)"
fi
