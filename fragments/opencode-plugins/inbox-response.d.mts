export type InboxResponse =
  | { ok: true; error: ''; messages: any[] }
  | { ok: false; error: string; messages: [] }

export function normalizeInboxResponse(response: unknown): InboxResponse
