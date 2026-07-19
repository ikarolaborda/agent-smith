const TOKEN_KEY = 'agent-smith.research-token';

export function getBearerToken(): string {
  return window.sessionStorage.getItem(TOKEN_KEY) ?? '';
}

export function setBearerToken(token: string): void {
  const value = token.trim();
  if (value) window.sessionStorage.setItem(TOKEN_KEY, value);
  else window.sessionStorage.removeItem(TOKEN_KEY);
}

export function authenticatedFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  const token = getBearerToken();
  if (token) headers.set('Authorization', `Bearer ${token}`);
  return fetch(input, { ...init, headers });
}
