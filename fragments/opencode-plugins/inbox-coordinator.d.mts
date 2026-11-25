// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

export interface DispatchEvent {
  id: string;
  type: string;
  source?: {
    id?: string;
    from?: string;
    target?: string;
    message_type?: string;
    [key: string]: any;
  };
  payload?: {
    prompt?: string | string[];
    command?: string;
    [key: string]: any;
  };
  actions?: {
    start_turn?: boolean;
    [key: string]: any;
  };
  [key: string]: any;
}

export interface EventPollResult {
  response: any;
  events: DispatchEvent[];
}

export interface InboxCoordinator {
  seed(events: unknown): void;
  poll(readEvents: () => any | Promise<any>): Promise<EventPollResult>;
  claim(events: unknown): DispatchEvent[];
  release(ids: unknown): void;
  markSubmitted(ids: unknown): void;
}

export function createEventCoordinator(): InboxCoordinator;
export function isSupportedEvent(event: unknown): boolean;
export function normalizeEventResponse(raw: unknown): {
  ok: boolean;
  error: string;
  protocol_version: number;
  events: DispatchEvent[];
};
