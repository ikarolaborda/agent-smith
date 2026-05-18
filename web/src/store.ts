import { Conversation, Message } from './types';

const STORAGE_KEY = 'agent-smith.conversations.v1';

export function loadConversations(): Conversation[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    return JSON.parse(raw) as Conversation[];
  } catch {
    return [];
  }
}

export function saveConversations(convs: Conversation[]): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(convs));
  } catch {
    /* quota exceeded — silently drop; UI surfaces nothing for now */
  }
}

export function newConversation(provider: string, model: string): Conversation {
  const now = Date.now();
  return {
    id: cryptoRandomId(),
    title: 'New chat',
    createdAt: now,
    updatedAt: now,
    provider,
    model,
    messages: [],
  };
}

export function deriveTitle(messages: Message[]): string {
  const firstUser = messages.find((m) => m.role === 'user');
  if (!firstUser) return 'New chat';
  const text = firstUser.content.trim().replace(/\s+/g, ' ');
  return text.length > 60 ? text.slice(0, 57) + '…' : text || 'New chat';
}

function cryptoRandomId(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) {
    return crypto.randomUUID();
  }
  return Math.random().toString(36).slice(2) + Date.now().toString(36);
}

export function makeMessageId(): string {
  return cryptoRandomId();
}
