# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright © 2026 Enrico Weigelt, metux IT consult
#
# Config sourced by the run-*.starfleetctl scripts.
#
# starfleetctl is the Go CLI consolidating the flock/race-prone
# fleet-coordination scripts (agent-bus, pr-claim, ws-commit) into one tool
# (mpbt-hq/starfleetctl, own repo). It is maintained as its OWN mpbt solution —
# cloned (and built via its Makefile) under _WORK_/starfleetctl/, deliberately
# NOT part of the xserver build — same pattern as go-x11proto and flyingtux.
export XLIBRE_RELEASE="starfleetctl"
export MPBT="mpbt-builder"

SOLUTION="cf/$XLIBRE_RELEASE/solutions/default.yaml"
WORKDIR="_WORK_/$XLIBRE_RELEASE"
