export function parsePresenceEmails(payload: unknown): string[] | null {
  if (!payload || typeof payload !== 'object') return null;

  const onlineClients = (payload as { onlineClients?: unknown }).onlineClients;
  if (!Array.isArray(onlineClients)) return null;

  const out: string[] = [];
  for (const value of onlineClients) {
    if (typeof value !== 'string' || value.length === 0) return null;
    out.push(value);
  }
  return out;
}

export function applyPresencePayload(
  payload: unknown,
  write: (onlineClients: string[]) => void,
): boolean {
  const onlineClients = parsePresenceEmails(payload);
  if (onlineClients === null) return false;
  write(onlineClients);
  return true;
}
