// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

export interface RuntimeIdentity {
  readonly version: string
  readonly pid: number
  readonly loadedAt: string
}

export interface FirstCommsReportInput {
  ship: string
  sessionID?: string | null
  messageIDs?: string[]
}

export function createRuntimeIdentity(
  version: string,
  pid: number,
  loadedAt: string,
): RuntimeIdentity

export function formatRuntimeIdentity(identity: RuntimeIdentity): string

export function createFirstCommsReporter(
  identity: RuntimeIdentity,
): (input: FirstCommsReportInput) => string | null
