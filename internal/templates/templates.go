// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package templates holds embedded source-data files that more than one
// starfleetctl subsystem needs to materialize on disk. Keeping them here
// gives every consumer a single source of truth.
package templates

import _ "embed"

// ProjectConfigRel is the in-repo path (relative to a workspace root) where the
// project configuration is installed.
const ProjectConfigRel = ".starfleet-ai/conf/project.yaml"

// ProjectConfigTemplate is the project configuration template, embedded so
// `bootstrap --fix` can create it if missing.
//
//go:embed project.yaml.template
var ProjectConfigTemplate []byte
