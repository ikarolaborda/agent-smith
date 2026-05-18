const PROFILE_KEY = 'agent-smith.profile.v1';

export function getOrCreateProfileId(): string {
  try {
    const existing = localStorage.getItem(PROFILE_KEY);
    if (existing) return existing;
  } catch {
    /* fall through */
  }
  const id =
    typeof crypto !== 'undefined' && 'randomUUID' in crypto
      ? `profile-${crypto.randomUUID()}`
      : `profile-${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`;
  try {
    localStorage.setItem(PROFILE_KEY, id);
  } catch {
    /* ignore quota errors */
  }
  return id;
}
