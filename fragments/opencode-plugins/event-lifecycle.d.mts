export function resolveLifecycleSessionID(lifecycle: { session_ref?: unknown } | null | undefined, fallbackSessionID?: string): string
export function deterministicMessageID(sessionID: string, eventIDs: string[]): string
export interface TurnCorrelationEntry {
  sessionID: string
  messageID: string
  metadata: any
  createdAt: number
}
export function createTurnCorrelationQueue(options?: { maxAgeMs?: number }): {
  capture(sessionID: string, messageID: string, metadata?: any, now?: number): boolean
  consume(sessionID: string, now?: number): TurnCorrelationEntry | null
  prune(now?: number): void
}

export type AssistantUpdateState = 'unrelated' | 'started' | 'completed' | 'failed'
export type RecoveredTurnState = 'active' | 'completed' | 'failed' | 'unknown'
export function classifyAssistantUpdate(info: any, expectedSessionID: string, expectedMessageID: string): AssistantUpdateState
export function classifyRecoveredTurn(assistantInfo: any, sessionStatus: any, expectedSessionID: string, expectedMessageID: string): RecoveredTurnState

export function completeEventBatch(
  eventIDs: unknown,
  transition: (id: string, state: 'submitted' | 'turn_started' | 'completed') => boolean,
): boolean;
