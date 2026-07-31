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
TSCONFIG=fragments/opencode-plugins/tsconfig.json
FAILED=0

note() { printf 'check-opencode-plugin: %s\n' "$*"; }

if [ ! -f "$PLUGIN" ]; then
    note "plugin not found: $PLUGIN — nothing to check"
    exit 0
fi

# --- 1) esbuild bundle check (syntax + import graph) ---
if command -v esbuild >/dev/null 2>&1; then
    if esbuild --bundle --format=esm --platform=node --log-level=error \
        --outfile=/dev/null "$PLUGIN"; then
        note "esbuild bundle check OK ($PLUGIN)"
    else
        note "esbuild bundle check FAILED"
        FAILED=1
    fi
else
    note "esbuild not found — skipping bundle check (install esbuild)"
fi

# --- 2) tsc --noEmit type check ---
# Pick a tsc: global install on PATH, or local via npx --no-install (never
# reaches the registry, so it stays offline-safe).
TSC=()
if command -v tsc >/dev/null 2>&1; then
    TSC=(tsc)
    TYPES_MODE=local-global
elif npx --no-install tsc --version >/dev/null 2>&1; then
    TSC=(npx --no-install tsc)
    TYPES_MODE=local
else
    TYPES_MODE=
fi

# Resolve @types/node the way the tsc we picked would. Without it tsc only
# reports TS2591 "Cannot find name 'process'" noise for every node global,
# so skip instead of failing.
types_ok=false
case "${TYPES_MODE:-}" in
    local-global)
        [ -d node_modules/@types/node ] && types_ok=true
        if [ "$types_ok" = false ] && command -v npm >/dev/null 2>&1; then
            g="$(npm root -g 2>/dev/null || true)"
            [ -n "$g" ] && [ -d "$g/@types/node" ] && types_ok=true
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
    if "${TSC[@]}" -p "$TSCONFIG"; then
        note "tsc --noEmit OK ($TSCONFIG)"
    else
        note "tsc --noEmit FAILED"
        FAILED=1
    fi
fi

exit "$FAILED"
