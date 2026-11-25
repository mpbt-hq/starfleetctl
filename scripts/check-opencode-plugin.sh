#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright © 2026 Enrico Weigelt, metux IT consult
#
# Best-effort static build-test for the opencode plugin TypeScript in
# fragments/opencode-plugins/. The bootstrap deploys the plugin as raw,
# un-compiled TypeScript to .opencode/plugins/, so syntax or type errors
# would only surface during real agent sessions — this script catches them
# at build time instead. Both checks are optional and skip (with a note) when
# their toolchain isn't installed, so `make all` stays green on hosts without
# node:
#
#   1) esbuild bundle check  — requires `esbuild` on PATH (syntax + import graph)
#   2) tsc --noEmit          — requires `typescript` AND `@types/node`
#                              (e.g. `npm i -g typescript @types/node`)

set -u
cd "$(dirname "$0")/.."

PLUGIN="${1:-fragments/opencode-plugins/starfleet-dispatch.ts}"
INTEGRATION_TEST="fragments/opencode-plugins/starfleet-dispatch.integration.test.ts"
TSCONFIG=fragments/opencode-plugins/tsconfig.json
FAILED=0

note() { printf 'check-opencode-plugin: %s\n' "$*"; }

check_file() {
    local file="$1"
    local label="$2"
    
    if [ ! -f "$file" ]; then
        note "$label not found: $file — skipping"
        return 0
    fi
    
    # --- 1) esbuild bundle check (syntax + import graph) ---
    if command -v esbuild >/dev/null 2>&1; then
        if esbuild --bundle --format=esm --platform=node --log-level=error \
            --outfile=/dev/null "$file"; then
            note "esbuild bundle check OK ($label)"
        else
            note "esbuild bundle check FAILED ($label)"
            return 1
        fi
    else
        note "esbuild not found — skipping bundle check for $label (install esbuild)"
    fi

    # --- 2) tsc --noEmit type check ---
    # We run tsc on the whole project (via -p) since both files are part of
    # the same tsconfig. Running tsc on individual files with -p is not supported.
    # So we only run tsc once for the project.
    return 0
}

# Check main plugin
if ! check_file "$PLUGIN" "plugin"; then
    FAILED=1
fi

# Check integration test (if exists)
if [ -f "$INTEGRATION_TEST" ]; then
    if ! check_file "$INTEGRATION_TEST" "integration test"; then
        FAILED=1
    fi
fi

# Run tsc once for the whole project
if command -v tsc >/dev/null 2>&1; then
    TSC=(tsc)
    TYPES_MODE=local-global
elif npx --no-install tsc --version >/dev/null 2>&1; then
    TSC=(npx --no-install tsc)
    TYPES_MODE=local
else
    TYPES_MODE=
fi

types_ok=false
TSC_EXTRA=()
case "${TYPES_MODE:-}" in
    local-global)
        if [ -d node_modules/@types/node ]; then
            types_ok=true
        elif command -v npm >/dev/null 2>&1; then
            g="$(npm root -g 2>/dev/null || true)"
            if [ -n "$g" ] && [ -d "$g/@types/node" ]; then
                types_ok=true
                TSC_EXTRA=(--typeRoots "$g/@types")
            fi
        fi
        ;;
    local)
        [ -d node_modules/@types/node ] && types_ok=true
        ;;
esac

if [ "${#TSC[@]}" -eq 0 ]; then
    note "typescript not found — skipping type check (npm i -g typescript @types/node)"
elif [ "$types_ok" = false ]; then
    note "typescript found, but @types/node missing — skipping type check (npm i -g @types/node)"
else
    if "${TSC[@]}" -p "$TSCONFIG" "${TSC_EXTRA[@]}"; then
        note "tsc --noEmit OK (project)"
    else
        note "tsc --noEmit FAILED (project)"
        FAILED=1
    fi
fi

exit "$FAILED"
