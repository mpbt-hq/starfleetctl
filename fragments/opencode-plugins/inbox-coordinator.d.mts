// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

export interface InboxMessage {
  id: string;
  from: string;
  text: string;
  type?: string;
  [key: string]: any;
}

export interface InboxPollResult {
  response: any;
  messages: InboxMessage[];
}

export interface InboxCoordinator {
  seed(messages: unknown): void;
  poll(readInbox: () => any | Promise<any>): Promise<InboxPollResult>;
  claim(messages: unknown): InboxMessage[];
  release(ids: unknown): void;
  markSubmitted(ids: unknown): void;
}

export function createInboxCoordinator(): InboxCoordinator;
export function isSupportedInboxMessage(message: unknown): boolean;
