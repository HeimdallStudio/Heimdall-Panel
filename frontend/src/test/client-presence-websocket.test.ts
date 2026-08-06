import { describe, expect, it, vi } from 'vitest';

import { keys } from '@/api/queryKeys';
import { websocketInvalidateQueryKey } from '@/api/websocketBridge';
import {
  applyPresencePayload,
  parsePresenceEmails,
} from '@/lib/clients/presence';

describe('client presence WebSocket hardening', () => {
  it('accepts an authoritative empty snapshot and clears the query cache', () => {
    const write = vi.fn();

    expect(applyPresencePayload({ onlineClients: [] }, write)).toBe(true);
    expect(write).toHaveBeenCalledOnce();
    expect(write).toHaveBeenCalledWith([]);
  });

  it('rejects malformed snapshots instead of poisoning the online cache', () => {
    const write = vi.fn();

    expect(parsePresenceEmails(null)).toBeNull();
    expect(parsePresenceEmails({})).toBeNull();
    expect(parsePresenceEmails({ onlineClients: ['alice', 7] })).toBeNull();
    expect(applyPresencePayload({ onlineClients: ['alice', 7] }, write)).toBe(false);
    expect(write).not.toHaveBeenCalled();
  });

  it('routes oversize presence invalidation to the online-client REST query', () => {
    expect(websocketInvalidateQueryKey('presence')).toEqual(
      keys.clients.onlines(),
    );
    expect(websocketInvalidateQueryKey('inbounds')).toEqual(
      keys.inbounds.root(),
    );
    expect(websocketInvalidateQueryKey('clients')).toEqual(
      keys.clients.root(),
    );
    expect(websocketInvalidateQueryKey('unknown')).toBeNull();
  });
});
