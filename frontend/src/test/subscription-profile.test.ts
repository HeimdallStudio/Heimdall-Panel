import { readFileSync } from 'node:fs';

import { describe, expect, it } from 'vitest';

import type { StreamSettings } from '@/schemas/api/inbound';
import {
  SubscriptionProfileRuntimeSchema,
  SubscriptionProfileSockoptSchema,
  type ExternalProxyEntry,
} from '@/schemas/protocols/stream/external-proxy';
import { TlsStreamSettingsSchema } from '@/schemas/protocols/security/tls';
import { XHttpStreamSettingsSchema } from '@/schemas/protocols/stream/xhttp';
import {
  findSubscriptionProfileRuntimeConflicts,
  planSubscriptionProfileRuntimeTopology,
} from '@/lib/xray/subscription-profile-runtime-bindings';
import {
  DEFAULT_SUBSCRIPTION_PROFILE_PORT,
  createSubscriptionProfileDraft,
  duplicateSubscriptionProfile,
  effectiveSubscriptionProfileStream,
  expandSubscriptionProfileEndpoints,
  hasAutomaticRuntimeMarker,
  isModernSubscriptionProfile,
  normalizeSubscriptionProfilesForProtocolSave,
  normalizeSubscriptionProfilesForSave,
  planDefaultSubscriptionPortSync,
  supportsSubscriptionProfiles,
} from '@/lib/xray/subscription-profile';

function baseStream(): StreamSettings {
  return {
    network: 'tcp',
    tcpSettings: {
      acceptProxyProtocol: false,
      header: { type: 'none' },
    },
    security: 'none',
  };
}

describe('subscription profile expansion', () => {

  it('keeps only the active transport and preserves Phase 2A transport fields', () => {
    const profile = {
      enabled: true,
      remark: 'ws-profile',
      dest: 'ws.example.com',
      port: 443,
      network: 'ws' as const,
      security: 'same' as const,
      forceTls: 'same' as const,
      tcpSettings: {
        acceptProxyProtocol: true,
        header: {
          type: 'http' as const,
          response: {
            version: '1.1',
            status: '204',
            reason: 'No Content',
            headers: { Server: ['edge'] },
          },
        },
      },
      wsSettings: {
        acceptProxyProtocol: true,
        path: '/ws',
        host: 'origin.example.com',
        headers: { 'X-Profile': 'ws' },
        heartbeatPeriod: 15,
      },
      grpcSettings: {
        serviceName: 'stale',
        authority: 'stale.example.com',
        multiMode: true,
      },
      httpupgradeSettings: {
        acceptProxyProtocol: true,
        path: '/stale',
        host: 'stale.example.com',
        headers: {},
      },
    };

    const effective = effectiveSubscriptionProfileStream(
      baseStream(),
      profile,
      'ws.example.com',
    );

    expect(effective.network).toBe('ws');
    if (effective.network !== 'ws') throw new Error('expected ws');
    expect(effective.wsSettings).toEqual(profile.wsSettings);
    expect('tcpSettings' in effective).toBe(false);
    expect('grpcSettings' in effective).toBe(false);
    expect('httpupgradeSettings' in effective).toBe(false);
  });

  it('preserves RAW response camouflage and transport proxy protocol', () => {
    const effective = effectiveSubscriptionProfileStream(
      baseStream(),
      {
        enabled: true,
        remark: 'raw-http',
        dest: 'raw.example.com',
        port: 443,
        network: 'tcp',
        security: 'same',
        forceTls: 'same',
        tcpSettings: {
          acceptProxyProtocol: true,
          header: {
            type: 'http',
            request: {
              version: '1.1',
              method: 'GET',
              path: ['/request'],
              headers: { Host: ['front.example.com'] },
            },
            response: {
              version: '1.1',
              status: '200',
              reason: 'OK',
              headers: { Server: ['nginx'] },
            },
          },
        },
      },
      'raw.example.com',
    );

    expect(effective.network).toBe('tcp');
    if (effective.network !== 'tcp') throw new Error('expected tcp');
    expect(effective.tcpSettings.acceptProxyProtocol).toBe(true);
    expect(effective.tcpSettings.header).toMatchObject({
      type: 'http',
      response: {
        version: '1.1',
        status: '200',
        reason: 'OK',
        headers: { Server: ['nginx'] },
      },
    });
  });
  it('accepts legacy runtime topology fields for read compatibility', () => {
    const parsed = SubscriptionProfileRuntimeSchema.parse({
      enabled: false,
      id: 'mobile-grpc',
      mode: 'shared',
      listen: '127.0.0.2',
      port: 8443,
    });

    expect(parsed).toMatchObject({
      enabled: false,
      id: 'mobile-grpc',
      mode: 'shared',
      listen: '127.0.0.2',
      port: 8443,
    });
  });

  it('creates a safe editable profile draft', () => {
    expect(createSubscriptionProfileDraft(8443)).toEqual({
      enabled: true,
      remark: '',
      dest: '',
      port: 8443,
      network: 'same',
      security: 'same',
      forceTls: 'same',
      overrideSniFromAddress: false,
      keepSniBlank: false,
      excludeFromSubTypes: [],
      mihomoX25519: false,
      shuffleHost: false,
      runtime: {},
    });
    expect(createSubscriptionProfileDraft().port).toBe(1995);
    expect(createSubscriptionProfileDraft(0).port).toBe(1995);
    expect(DEFAULT_SUBSCRIPTION_PROFILE_PORT).toBe(1995);
  });

  it('recognizes structured profiles while leaving legacy entries subscription-only', () => {
    expect(isModernSubscriptionProfile(createSubscriptionProfileDraft())).toBe(true);
    expect(isModernSubscriptionProfile({
      remark: 'legacy',
      dest: 'cdn.example.com',
      port: 443,
      forceTls: 'tls',
    })).toBe(false);
  });

  it('uses a hidden marker to separate new automatic listeners from older structured subscription-only rows', () => {
    const draft = createSubscriptionProfileDraft(1995);
    expect(hasAutomaticRuntimeMarker(draft)).toBe(true);

    const preAutomaticTopology: ExternalProxyEntry = {
      enabled: true,
      remark: 'existing structured subscription',
      dest: '',
      port: 1995,
      network: 'grpc',
      forceTls: 'same',
    };
    expect(isModernSubscriptionProfile(preAutomaticTopology)).toBe(true);
    expect(hasAutomaticRuntimeMarker(preAutomaticTopology)).toBe(false);
  });

  it('drops stale hidden profiles for protocols that cannot own Multi Profile runtime listeners', () => {
    const staleProfiles = [createSubscriptionProfileDraft(8443)];

    expect(supportsSubscriptionProfiles('vless')).toBe(true);
    expect(supportsSubscriptionProfiles('hysteria')).toBe(true);
    expect(supportsSubscriptionProfiles('wireguard')).toBe(false);
    expect(supportsSubscriptionProfiles('tunnel')).toBe(false);
    expect(normalizeSubscriptionProfilesForProtocolSave('wireguard', staleProfiles)).toEqual([]);
    expect(normalizeSubscriptionProfilesForProtocolSave('tunnel', staleProfiles)).toEqual([]);
    expect(normalizeSubscriptionProfilesForProtocolSave('vless', staleProfiles)).toHaveLength(1);
  });

  it('preserves hidden IDs and strips deprecated topology controls before Save', () => {
    const [profile] = normalizeSubscriptionProfilesForSave([{
      ...createSubscriptionProfileDraft(8443),
      runtime: {
        enabled: false,
        id: '  stable-id  ',
        mode: 'shared',
        listen: '127.0.0.2',
        port: 9443,
      },
    }]);

    expect(profile.runtime).toEqual({ id: 'stable-id' });
    expect(profile.port).toBe(8443);
  });

  it('preserves duplicate hidden IDs for the backend source of truth to reject', () => {
    const profiles = normalizeSubscriptionProfilesForSave([
      {
        ...createSubscriptionProfileDraft(8443),
        runtime: { id: 'duplicate-id' },
      },
      {
        ...createSubscriptionProfileDraft(9443),
        runtime: { id: 'duplicate-id' },
      },
    ]);

    expect(profiles.map((profile) => profile.runtime?.id)).toEqual([
      'duplicate-id',
      'duplicate-id',
    ]);
  });

  it('keeps automatic runtime normalization idempotent and delegates missing IDs', () => {
    const first = normalizeSubscriptionProfilesForSave([
      createSubscriptionProfileDraft(8443),
    ]);
    const second = normalizeSubscriptionProfilesForSave(first);

    expect(first[0].runtime).toEqual({});
    expect(second).toEqual(first);
  });

  it('does not materialize an ID for a disabled draft', () => {
    const [profile] = normalizeSubscriptionProfilesForSave([{
      ...createSubscriptionProfileDraft(8443),
      enabled: false,
    }]);

    expect(profile.runtime).toEqual({});
  });

  it('leaves disabled runtime metadata untouched until the profile is activated', () => {
    const [profile] = normalizeSubscriptionProfilesForSave([{
      ...createSubscriptionProfileDraft(8443),
      enabled: false,
      runtime: {
        enabled: false,
        id: 'disabled-profile',
        mode: 'shared',
        listen: '127.0.0.2',
        port: 9443,
      },
    }]);

    expect(profile.runtime).toEqual({
      enabled: false,
      id: 'disabled-profile',
      mode: 'shared',
      listen: '127.0.0.2',
      port: 9443,
    });
  });

  it('does not convert a legacy subscription-only entry into runtime metadata', () => {
    const [profile] = normalizeSubscriptionProfilesForSave([{
      remark: 'legacy',
      dest: 'cdn.example.com',
      port: 443,
      forceTls: 'tls',
    }]);

    expect(profile.runtime).toBeUndefined();
  });

  it('preserves an empty runtime marker for migrated runtime-only profiles', () => {
    const [profile] = normalizeSubscriptionProfilesForSave([{
      enabled: true,
      remark: 'runtime-only migration',
      dest: '',
      port: 8443,
      forceTls: 'same',
      runtime: {
        enabled: false,
        mode: 'shared',
        listen: '127.0.0.2',
        port: 9443,
      },
    }]);

    expect(profile.runtime).toEqual({});
    expect(isModernSubscriptionProfile(profile)).toBe(true);
  });

  it('leaves malformed runtime metadata visible to schema validation', () => {
    const malformed = {
      ...createSubscriptionProfileDraft(8443),
      runtime: 'not-an-object',
    } as unknown as ExternalProxyEntry;
    const [profile] = normalizeSubscriptionProfilesForSave([malformed]);

    expect((profile as unknown as { runtime: unknown }).runtime).toBe('not-an-object');
  });

  it('does not plan a listener for a markerless structured migration row', () => {
    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '0.0.0.0',
      parentPort: 22937,
      parentStreamSettings: baseStream(),
      profiles: [{
        enabled: true,
        remark: 'pre-automatic topology',
        dest: '',
        port: 1995,
        network: 'grpc',
        forceTls: 'same',
      }],
    });

    expect(plan).toEqual({ profiles: [], conflicts: [] });
  });

  it('allows an exact inherited runtime endpoint to reuse the parent listener', () => {
    const conflicts = findSubscriptionProfileRuntimeConflicts({
      parentListen: '0.0.0.0',
      parentPort: 23587,
      parentStreamSettings: baseStream(),
      profiles: [{
        ...createSubscriptionProfileDraft(23587),
        network: 'same',
        security: 'same',
        runtime: {
          enabled: true,
          id: 'same-parent',
          mode: 'direct',
          listen: '',
        },
      }],
    });

    expect(conflicts).toEqual([]);
  });

  it('automatically shares a changed TCP-family profile on the parent socket', () => {
    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '0.0.0.0',
      parentPort: 23587,
      parentStreamSettings: baseStream(),
      profiles: [{
        ...createSubscriptionProfileDraft(23587),
        network: 'grpc',
        security: 'same',
        grpcSettings: {
          serviceName: 'mobile',
          authority: '',
          multiMode: false,
        },
        runtime: {
          enabled: true,
          id: 'grpc-parent-conflict',
          mode: 'direct',
          listen: '',
        },
      }],
    });

    expect(plan.conflicts).toEqual([]);
    expect(plan.profiles).toMatchObject([{
      profileIndex: 0,
      mode: 'shared',
      listen: '0.0.0.0',
      port: 23587,
      transport: 'tcp',
    }]);
  });

  it('ignores legacy runtime.listen and derives the parent listen automatically', () => {
    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '0.0.0.0',
      parentPort: 23587,
      parentStreamSettings: baseStream(),
      profiles: [{
        ...createSubscriptionProfileDraft(23587),
        network: 'grpc',
        runtime: {
          id: 'legacy-listen',
          listen: '127.0.0.1',
        },
      }],
    });

    expect(plan.conflicts).toEqual([]);
    expect(plan.profiles[0]).toMatchObject({
      mode: 'shared',
      listen: '0.0.0.0',
      port: 23587,
    });
  });

  it('ignores legacy runtime.port and uses the advertised profile port', () => {
    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '127.0.0.1',
      parentPort: 23587,
      parentStreamSettings: baseStream(),
      profiles: [{
        ...createSubscriptionProfileDraft(2087),
        network: 'grpc',
        runtime: {
          id: 'legacy-port',
          port: 9443,
        },
      }],
    });

    expect(plan.conflicts).toEqual([]);
    expect(plan.profiles[0]).toMatchObject({
      mode: 'direct',
      listen: '127.0.0.1',
      port: 2087,
    });
  });

  it('ignores legacy mapped-listen metadata and uses the parent socket', () => {
    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '127.0.0.1',
      parentPort: 23587,
      parentStreamSettings: baseStream(),
      profiles: [{
        ...createSubscriptionProfileDraft(23587),
        network: 'grpc',
        runtime: {
          enabled: true,
          id: 'mapped-address',
          mode: 'direct',
          listen: '::ffff:127.0.0.1',
        },
      }],
    });

    expect(plan.conflicts).toEqual([]);
    expect(plan.profiles[0]).toMatchObject({
      mode: 'shared',
      listen: '127.0.0.1',
      port: 23587,
    });
  });

  it('does not collide across TCP and UDP transport families', () => {
    const conflicts = findSubscriptionProfileRuntimeConflicts({
      parentListen: '0.0.0.0',
      parentPort: 23587,
      parentStreamSettings: {
        network: 'kcp',
        security: 'none',
      },
      profiles: [{
        ...createSubscriptionProfileDraft(23587),
        network: 'tcp',
        runtime: {
          enabled: true,
          id: 'tcp-on-udp-port',
          mode: 'direct',
          listen: '',
        },
      }],
    });

    expect(conflicts).toEqual([]);
  });

  it('automatically shares sibling TCP profiles with unambiguous selectors', () => {
    const runtime = (id: string) => ({
      enabled: true,
      id,
      mode: 'direct' as const,
      listen: '127.0.0.1',
      port: 1995,
    });
    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '0.0.0.0',
      parentPort: 23587,
      parentStreamSettings: baseStream(),
      profiles: [
        {
          ...createSubscriptionProfileDraft(1995),
          network: 'grpc',
          runtime: runtime('first'),
        },
        {
          ...createSubscriptionProfileDraft(1995),
          network: 'ws',
          runtime: runtime('second'),
        },
      ],
    });

    expect(plan.conflicts).toEqual([]);
    expect(plan.profiles).toMatchObject([
      { profileIndex: 0, mode: 'shared', listen: '0.0.0.0', port: 1995 },
      { profileIndex: 1, mode: 'shared', listen: '0.0.0.0', port: 1995 },
    ]);
  });

  it('shares all five TCP transports by unique TLS SNI while mKCP uses UDP on the same number', () => {
    const parentStream: StreamSettings = {
      ...baseStream(),
      security: 'tls',
      tlsSettings: TlsStreamSettingsSchema.parse({
        serverName: '',
        alpn: [],
      }),
    };
    const runtime = (id: string) => ({ enabled: true, id, mode: 'direct' as const, listen: '' });
    const tlsProfile = (
      network: 'tcp' | 'ws' | 'grpc' | 'httpupgrade' | 'xhttp',
      id: string,
      serverName: string,
    ) => ({
      ...createSubscriptionProfileDraft(443),
      network,
      security: 'tls' as const,
      tlsSettings: {
        serverName,
        alpn: [],
        settings: {
          fingerprint: 'chrome' as const,
          echConfigList: '',
          pinnedPeerCertSha256: [],
          verifyPeerCertByName: '',
          allowInsecure: false,
        },
      },
      runtime: runtime(id),
    });
    const profiles = [
      { ...tlsProfile('tcp', 'raw', 'raw.example.com'), runtime: { enabled: false, id: 'raw', mode: 'direct' as const, listen: '' } },
      { ...tlsProfile('ws', 'ws', 'ws.example.com'), wsSettings: { acceptProxyProtocol: false, path: '/ws', host: '', headers: {}, heartbeatPeriod: 0 } },
      { ...tlsProfile('grpc', 'grpc', 'grpc.example.com'), grpcSettings: { serviceName: 'grpc', authority: '', multiMode: false } },
      { ...tlsProfile('httpupgrade', 'hu', 'hu.example.com'), httpupgradeSettings: { acceptProxyProtocol: false, path: '/upgrade', host: '', headers: {} } },
      {
        ...tlsProfile('xhttp', 'xhttp', 'xhttp.example.com'),
        xhttpSettings: XHttpStreamSettingsSchema.parse({ mode: 'auto', path: '/xhttp' }),
      },
      { ...createSubscriptionProfileDraft(443), network: 'kcp' as const, security: 'none' as const, runtime: runtime('kcp') },
    ];

    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '0.0.0.0',
      parentPort: 443,
      parentStreamSettings: parentStream,
      profiles,
    });

    expect(plan.conflicts).toEqual([]);
    expect(plan.profiles.filter((profile) => profile.mode === 'shared')).toHaveLength(4);
    expect(plan.profiles.find((profile) => profile.profileIndex === 5)).toMatchObject({
      mode: 'direct',
      transport: 'udp',
      port: 443,
    });
  });

  it('rejects inherited ECH on the mandatory parent shared route', () => {
    const parentStream: StreamSettings = {
      ...baseStream(),
      security: 'tls',
      tlsSettings: TlsStreamSettingsSchema.parse({
        serverName: 'raw.example.com',
        alpn: [],
        settings: {
          echConfigList: 'ECH-CONFIG',
        },
      }),
    };
    const profile = {
      ...createSubscriptionProfileDraft(443),
      network: 'ws' as const,
      security: 'tls' as const,
      tlsSettings: {
        serverName: 'ws.example.com',
        alpn: [],
        settings: {
          fingerprint: 'chrome' as const,
          echConfigList: '',
          pinnedPeerCertSha256: [],
          verifyPeerCertByName: '',
          allowInsecure: false,
        },
      },
      wsSettings: {
        acceptProxyProtocol: false,
        path: '/ws',
        host: '',
        headers: {},
        heartbeatPeriod: 0,
      },
      runtime: { enabled: true, id: 'ws', mode: 'direct' as const, listen: '' },
    };
    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '0.0.0.0',
      parentPort: 443,
      parentStreamSettings: parentStream,
      profiles: [profile],
    });

    expect(plan.conflicts[0]).toMatchObject({
      profileIndex: 0,
      code: 'ech_unsupported',
    });
    expect(plan.profiles[0]?.mode).toBe('invalid');
  });

  it('rejects duplicate TLS SNI on a shared socket for every involved profile', () => {
    const runtime = (id: string) => ({ enabled: true, id, mode: 'direct' as const, listen: '' });
    const profile = (id: string, network: 'ws' | 'grpc') => ({
      ...createSubscriptionProfileDraft(443),
      network,
      security: 'tls' as const,
      tlsSettings: {
        serverName: 'same.example.com',
        alpn: [],
        settings: {
          fingerprint: 'chrome' as const,
          echConfigList: '',
          pinnedPeerCertSha256: [],
          verifyPeerCertByName: '',
          allowInsecure: false,
        },
      },
      runtime: runtime(id),
    });
    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '127.0.0.1',
      parentPort: 23587,
      parentStreamSettings: baseStream(),
      profiles: [
        { ...profile('ws', 'ws'), runtime: { ...runtime('ws'), listen: '127.0.0.2' }, wsSettings: { acceptProxyProtocol: false, path: '/ws', host: '', headers: {}, heartbeatPeriod: 0 } },
        { ...profile('grpc', 'grpc'), runtime: { ...runtime('grpc'), listen: '127.0.0.2' }, grpcSettings: { serviceName: 'grpc', authority: '', multiMode: false } },
      ],
    });

    expect(plan.conflicts.map((conflict) => conflict.code)).toEqual([
      'duplicate_sni',
      'duplicate_sni',
    ]);
    expect(plan.profiles.every((profilePlan) => profilePlan.mode === 'invalid')).toBe(true);
  });

  it('rejects a shared TLS group when the mandatory parent has no exact SNI', () => {
    const profile = {
      ...createSubscriptionProfileDraft(443),
      network: 'ws' as const,
      security: 'tls' as const,
      tlsSettings: {
        serverName: 'ws.example.com',
        alpn: [],
        settings: {
          fingerprint: 'chrome' as const,
          echConfigList: '',
          pinnedPeerCertSha256: [],
          verifyPeerCertByName: '',
          allowInsecure: false,
        },
      },
      wsSettings: { acceptProxyProtocol: false, path: '/ws', host: '', headers: {}, heartbeatPeriod: 0 },
      runtime: { enabled: true, id: 'ws', mode: 'direct' as const, listen: '' },
    };
    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '0.0.0.0',
      parentPort: 443,
      parentStreamSettings: {
        ...baseStream(),
        security: 'tls',
        tlsSettings: TlsStreamSettingsSchema.parse({
          serverName: '',
          alpn: [],
        }),
      },
      profiles: [
        createSubscriptionProfileDraft(443),
        profile,
      ],
    });

    expect(plan.conflicts[0]).toMatchObject({ profileIndex: 1, code: 'missing_sni' });
    expect(plan.profiles.find(({ profileIndex }) => profileIndex === 1)?.mode).toBe('invalid');
  });

  it('rejects overlapping cleartext HTTP Host/Path selectors', () => {
    const runtime = (id: string) => ({ enabled: true, id, mode: 'direct' as const, listen: '127.0.0.2', port: 1995 });
    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '127.0.0.1',
      parentPort: 23587,
      parentStreamSettings: baseStream(),
      profiles: [
        {
          ...createSubscriptionProfileDraft(1995),
          network: 'ws',
          wsSettings: { acceptProxyProtocol: false, path: '/shared', host: 'edge.example.com', headers: {}, heartbeatPeriod: 0 },
          runtime: runtime('ws'),
        },
        {
          ...createSubscriptionProfileDraft(1995),
          network: 'httpupgrade',
          httpupgradeSettings: { acceptProxyProtocol: false, path: '/shared', host: 'EDGE.EXAMPLE.COM:443', headers: {} },
          runtime: runtime('hu'),
        },
      ],
    });

    expect(plan.conflicts.map((conflict) => conflict.code)).toEqual([
      'http_selector_overlap',
      'http_selector_overlap',
    ]);
  });

  it('rejects malformed cleartext HTTP selectors before Save', () => {
    const runtime = (id: string) => ({
      enabled: true,
      id,
      mode: 'direct' as const,
      listen: '127.0.0.2',
      port: 1995,
    });
    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '127.0.0.1',
      parentPort: 23587,
      parentStreamSettings: baseStream(),
      profiles: [
        {
          ...createSubscriptionProfileDraft(1995),
          network: 'ws',
          wsSettings: {
            acceptProxyProtocol: false,
            path: '/ws?tenant=1',
            host: 'bad host.example.com',
            headers: {},
            heartbeatPeriod: 0,
          },
          runtime: runtime('invalid-http'),
        },
        {
          ...createSubscriptionProfileDraft(1995),
          network: 'grpc',
          grpcSettings: { serviceName: 'grpc', authority: '', multiMode: false },
          runtime: runtime('grpc'),
        },
      ],
    });

    expect(plan.conflicts[0]).toMatchObject({
      profileIndex: 0,
      code: 'invalid_http_selector',
    });
    expect(plan.profiles.find((profile) => profile.profileIndex === 0)?.mode).toBe('invalid');
  });

  it('canonicalizes IDNA HTTP hosts before overlap detection', () => {
    const runtime = (id: string) => ({
      enabled: true,
      id,
      mode: 'direct' as const,
      listen: '127.0.0.2',
      port: 1995,
    });
    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '127.0.0.1',
      parentPort: 23587,
      parentStreamSettings: baseStream(),
      profiles: [
        {
          ...createSubscriptionProfileDraft(1995),
          network: 'ws',
          wsSettings: {
            acceptProxyProtocol: false,
            path: '/ws',
            host: 'BÜCHER.example:443',
            headers: {},
            heartbeatPeriod: 0,
          },
          runtime: runtime('ws'),
        },
        {
          ...createSubscriptionProfileDraft(1995),
          network: 'httpupgrade',
          httpupgradeSettings: {
            acceptProxyProtocol: false,
            path: '/ws',
            host: 'xn--bcher-kva.example',
            headers: {},
          },
          runtime: runtime('hu'),
        },
      ],
    });

    expect(plan.conflicts.map((conflict) => conflict.code)).toEqual([
      'http_selector_overlap',
      'http_selector_overlap',
    ]);
  });

  it('collapses two same-port mKCP profiles into one shared UDP runtime', () => {
    const runtime = (id: string) => ({ enabled: true, id, mode: 'direct' as const, listen: '127.0.0.2', port: 1995 });
    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '127.0.0.1',
      parentPort: 23587,
      parentStreamSettings: baseStream(),
      profiles: [
        {
          ...createSubscriptionProfileDraft(1995),
          network: 'kcp',
          kcpSettings: { mtu: 1200, tti: 20, uplinkCapacity: 5, downlinkCapacity: 20, cwndMultiplier: 1, maxSendingWindow: 2097152 },
          runtime: runtime('kcp-a'),
        },
        {
          ...createSubscriptionProfileDraft(1995),
          network: 'kcp',
          kcpSettings: { mtu: 1350, tti: 50, uplinkCapacity: 10, downlinkCapacity: 40, cwndMultiplier: 2, maxSendingWindow: 4194304 },
          runtime: runtime('kcp-b'),
        },
      ],
    });

    expect(plan.conflicts).toEqual([]);
    expect(plan.profiles.map((profile) => profile.mode)).toEqual(['shared', 'shared']);
  });

  it('ignores disabled profiles but legacy runtime.enabled cannot disable topology', () => {
    const plan = planSubscriptionProfileRuntimeTopology({
      parentListen: '0.0.0.0',
      parentPort: 23587,
      parentStreamSettings: baseStream(),
      profiles: [
        {
          ...createSubscriptionProfileDraft(23587),
          enabled: false,
          network: 'grpc',
          runtime: {
            enabled: true,
            id: 'disabled-profile',
          },
        },
        {
          ...createSubscriptionProfileDraft(23587),
          network: 'grpc',
          runtime: {
            enabled: false,
            id: 'automatic-runtime',
          },
        },
      ],
    });

    expect(plan.conflicts).toEqual([]);
    expect(plan.profiles).toMatchObject([{
      profileIndex: 1,
      mode: 'shared',
      listen: '0.0.0.0',
      port: 23587,
    }]);
  });

  it('does not treat a missing parent transport object as exact listener reuse', () => {
    const conflicts = findSubscriptionProfileRuntimeConflicts({
      parentListen: '0.0.0.0',
      parentPort: 23587,
      parentStreamSettings: { network: 'tcp', security: 'none' },
      profiles: [{
        ...createSubscriptionProfileDraft(23587),
        runtime: {
          enabled: true,
          id: 'materialized-transport',
          mode: 'direct',
          listen: '',
        },
      }],
    });

    expect(conflicts[0]).toMatchObject({ kind: 'parent', port: 23587 });
  });

  it('clears hidden topology identity when duplicating a profile', () => {
    const original = {
      ...createSubscriptionProfileDraft(2087),
      runtime: {
        enabled: true,
        id: 'stable-runtime-id',
        mode: 'direct' as const,
        listen: '',
      },
    };
    const duplicate = duplicateSubscriptionProfile(original);
    expect(duplicate.runtime).toEqual({});
    expect(duplicate.runtime?.id).toBeUndefined();
    expect(duplicate.runtime).not.toHaveProperty('enabled');
    expect(duplicate.runtime).not.toHaveProperty('mode');
    expect(duplicate.runtime).not.toHaveProperty('listen');
    expect(duplicate.runtime).not.toHaveProperty('port');
    expect(duplicate.port).toBe(original.port);
  });

  it('keeps the legacy single default configuration when profiles are absent', () => {
    const endpoints = expandSubscriptionProfileEndpoints(baseStream(), 'node.example.com', 27543);

    expect(endpoints).toHaveLength(1);
    expect(endpoints[0]).toMatchObject({
      address: 'node.example.com',
      port: 27543,
      remark: '',
      profile: null,
    });
    expect(endpoints[0].streamSettings.externalProxy).toBeUndefined();
  });

  it('inherits the resolved share address when a profile destination is blank', () => {
    const stream: StreamSettings = {
      ...baseStream(),
      externalProxy: [
        {
          enabled: true,
          remark: 'empty',
          dest: '',
          port: 443,
          network: 'same',
          security: 'same',
          forceTls: 'same',
        },
        {
          enabled: true,
          remark: 'whitespace',
          dest: '   ',
          port: 8443,
          network: 'same',
          security: 'same',
          forceTls: 'same',
        },
        {
          enabled: true,
          remark: 'override',
          dest: 'cdn.example.com',
          port: 9443,
          network: 'same',
          security: 'same',
          forceTls: 'same',
        },
      ],
    };

    const endpoints = expandSubscriptionProfileEndpoints(
      stream,
      'resolved-node.example.com',
      27543,
    );

    expect(endpoints.map((endpoint) => endpoint.address)).toEqual([
      'resolved-node.example.com',
      'resolved-node.example.com',
      'cdn.example.com',
    ]);
  });

  it('filters disabled profiles and applies independent WS/TLS settings', () => {
    const stream: StreamSettings = {
      ...baseStream(),
      externalProxy: [
        {
          enabled: false,
          remark: 'disabled',
          dest: 'disabled.example.com',
          port: 443,
          network: 'same',
          security: 'same',
          forceTls: 'same',
        },
        {
          enabled: true,
          remark: 'ws-tls',
          dest: 'cdn.example.com',
          port: 8443,
          network: 'ws',
          security: 'tls',
          forceTls: 'same',
          wsSettings: {
            acceptProxyProtocol: false,
            path: '/secx',
            host: 'origin.example.com',
            headers: {},
            heartbeatPeriod: 0,
          },
          tlsSettings: {
            serverName: 'sni.example.com',
            alpn: ['h2'],
            settings: {
              fingerprint: 'chrome',
              echConfigList: '',
              pinnedPeerCertSha256: [],
      verifyPeerCertByName: '',
              allowInsecure: true,
            },
          },
        },
      ],
    };

    const endpoints = expandSubscriptionProfileEndpoints(stream, 'node.example.com', 27543);

    expect(endpoints).toHaveLength(1);
    expect(endpoints[0].address).toBe('cdn.example.com');
    expect(endpoints[0].port).toBe(8443);
    expect(endpoints[0].streamSettings.network).toBe('ws');
    expect(endpoints[0].streamSettings.security).toBe('tls');
    if (endpoints[0].streamSettings.network !== 'ws') throw new Error('expected ws');
    expect(endpoints[0].streamSettings.wsSettings.path).toBe('/secx');
    expect('tcpSettings' in endpoints[0].streamSettings).toBe(false);
    if (endpoints[0].streamSettings.security !== 'tls') throw new Error('expected tls');
    expect(endpoints[0].streamSettings.tlsSettings.serverName).toBe('sni.example.com');
  });

  it('preserves a profile-specific Mux override for JSON subscription generation', () => {
    const stream: StreamSettings = {
      ...baseStream(),
      externalProxy: [
        {
          enabled: true,
          remark: 'mux',
          dest: 'mux.example.com',
          port: 443,
          network: 'same',
          security: 'same',
          forceTls: 'same',
          mux: {
            enabled: true,
            concurrency: 4,
            xudpConcurrency: 8,
            xudpProxyUDP443: 'allow',
          },
        },
      ],
    };

    const [endpoint] = expandSubscriptionProfileEndpoints(stream, 'node.example.com', 27543);
    expect(endpoint.profile?.mux).toEqual({
      enabled: true,
      concurrency: 4,
      xudpConcurrency: 8,
      xudpProxyUDP443: 'allow',
    });
  });

  it('expands two inbounds with three profiles into exactly six endpoints', () => {
    const withProfiles = (prefix: string): StreamSettings => ({
      ...baseStream(),
      externalProxy: [1, 2, 3].map((index) => ({
        enabled: true,
        remark: `${prefix}${index}`,
        dest: `${prefix}${index}.example.com`,
        port: 443,
        network: 'same' as const,
        security: 'same' as const,
        forceTls: 'same' as const,
      })),
    });

    const total = [withProfiles('a'), withProfiles('b')]
      .flatMap((stream) => expandSubscriptionProfileEndpoints(stream, 'node.example.com', 27543));
    expect(total).toHaveLength(6);
  });

  it('returns no configurations when a configured profile list is fully disabled', () => {
    const stream: StreamSettings = {
      ...baseStream(),
      externalProxy: [
        {
          enabled: false,
          remark: 'one',
          dest: 'one.example.com',
          port: 443,
          network: 'same',
          security: 'same',
          forceTls: 'same',
        },
      ],
    };

    expect(expandSubscriptionProfileEndpoints(stream, 'node.example.com', 27543)).toEqual([]);
  });

  it('keeps legacy forceTls/SNI fields working', () => {
    const stream: StreamSettings = {
      ...baseStream(),
      externalProxy: [
        {
          enabled: true,
          remark: 'legacy',
          dest: 'legacy.example.com',
          port: 443,
          network: 'same',
          security: 'same',
          forceTls: 'tls',
          sni: 'sni.example.com',
          fingerprint: 'firefox',
          alpn: ['h2'],
        },
      ],
    };

    const [endpoint] = expandSubscriptionProfileEndpoints(stream, 'node.example.com', 27543);
    expect(endpoint.streamSettings.security).toBe('tls');
    if (endpoint.streamSettings.security !== 'tls') throw new Error('expected tls');
    expect(endpoint.streamSettings.tlsSettings.serverName).toBe('sni.example.com');
    expect(endpoint.streamSettings.tlsSettings.settings.fingerprint).toBe('firefox');
  });

  it('applies profile Mux to the effective client stream', () => {
    const stream: StreamSettings = {
      ...baseStream(),
      externalProxy: [{
        enabled: true,
        remark: 'mux-runtime',
        dest: 'mux.example.com',
        port: 443,
        network: 'same',
        security: 'same',
        forceTls: 'same',
        mux: {
          enabled: true,
          concurrency: 4,
          xudpConcurrency: 8,
          xudpProxyUDP443: 'allow',
        },
      }],
    };

    const [endpoint] = expandSubscriptionProfileEndpoints(
      stream,
      'node.example.com',
      27543,
    );

    expect(
      (endpoint.streamSettings as unknown as { mux?: unknown }).mux,
    ).toEqual({
      enabled: true,
      concurrency: 4,
      xudpConcurrency: 8,
      xudpProxyUDP443: 'allow',
    });
  });

  it('applies SNI modes and legacy certificate-name verification', () => {
    const overrideStream: StreamSettings = {
      ...baseStream(),
      externalProxy: [{
        enabled: true,
        remark: 'override-sni',
        dest: '',
        port: 443,
        network: 'same',
        security: 'tls',
        forceTls: 'same',
        overrideSniFromAddress: true,
        verifyPeerCertByName: 'verify.example.com',
      }],
    };

    const [overrideEndpoint] = expandSubscriptionProfileEndpoints(
      overrideStream,
      'resolved.example.com',
      443,
    );

    if (overrideEndpoint.streamSettings.security !== 'tls') {
      throw new Error('expected tls');
    }

    expect(
      overrideEndpoint.streamSettings.tlsSettings.serverName,
    ).toBe('resolved.example.com');
    expect(
      overrideEndpoint.streamSettings.tlsSettings.settings
        .verifyPeerCertByName,
    ).toBe('verify.example.com');

    const blankStream: StreamSettings = {
      ...baseStream(),
      externalProxy: [{
        enabled: true,
        remark: 'blank-sni',
        dest: 'edge.example.com',
        port: 443,
        network: 'same',
        security: 'tls',
        forceTls: 'same',
        keepSniBlank: true,
        tlsSettings: {
          serverName: 'must-be-cleared.example.com',
          alpn: [],
          settings: {
            fingerprint: 'chrome',
            echConfigList: '',
            pinnedPeerCertSha256: [],
            verifyPeerCertByName: '',
            allowInsecure: false,
          },
        },
      }],
    };

    const [blankEndpoint] = expandSubscriptionProfileEndpoints(
      blankStream,
      'resolved.example.com',
      443,
    );

    if (blankEndpoint.streamSettings.security !== 'tls') {
      throw new Error('expected tls');
    }

    expect(
      blankEndpoint.streamSettings.tlsSettings.serverName,
    ).toBe('');
  });

  it('applies client Sockopt and strips listener-only keys', () => {
    const sockopt = SubscriptionProfileSockoptSchema.parse({
      mark: 27,
      tcpFastOpen: true,
      tcpKeepAliveInterval: 15,
      tcpKeepAliveIdle: 90,
      tcpMaxSeg: 1400,
      tcpUserTimeout: 30000,
      tcpWindowClamp: 65535,
      tcpcongestion: 'bbr',
      tproxy: 'off',
      penetrate: true,
      domainStrategy: 'UseIP',
      customSockopt: [{
        system: 'linux',
        level: '6',
        opt: '19',
        type: 'int',
        value: '1',
      }],
      acceptProxyProtocol: true,
      V6Only: true,
      trustedXForwardedFor: ['127.0.0.1'],
    });

    expect(sockopt).not.toHaveProperty('acceptProxyProtocol');
    expect(sockopt).not.toHaveProperty('V6Only');
    expect(sockopt).not.toHaveProperty('trustedXForwardedFor');

    const stream: StreamSettings = {
      ...baseStream(),
      externalProxy: [{
        enabled: true,
        remark: 'sockopt',
        dest: 'sockopt.example.com',
        port: 443,
        network: 'same',
        security: 'same',
        forceTls: 'same',
        sockopt,
      }],
    };

    const [endpoint] = expandSubscriptionProfileEndpoints(
      stream,
      'node.example.com',
      27543,
    );

    expect(
      (endpoint.streamSettings as unknown as {
        sockopt?: Record<string, unknown>;
      }).sockopt,
    ).toMatchObject({
      mark: 27,
      tcpFastOpen: true,
      tcpKeepAliveInterval: 15,
      tcpKeepAliveIdle: 90,
      tcpMaxSeg: 1400,
      tcpUserTimeout: 30000,
      tcpWindowClamp: 65535,
      tcpcongestion: 'bbr',
      tproxy: 'off',
      penetrate: true,
      domainStrategy: 'UseIP',
      customSockopt: [{
        system: 'linux',
        level: '6',
        opt: '19',
        type: 'int',
        value: '1',
      }],
    });
  });
});

describe('default subscription profile port synchronization', () => {
  it('updates the default profile when the inbound port changes', () => {
    const initial = planDefaultSubscriptionPortSync(null, {
      inboundPort: 49362,
      profilePort: 49362,
    });
    const changed = planDefaultSubscriptionPortSync(initial.state, {
      inboundPort: 1995,
      profilePort: 49362,
    });

    expect(initial.state.linked).toBe(true);
    expect(changed.setProfilePort).toBe(1995);
    expect(changed.setInboundPort).toBeUndefined();
  });

  it('updates the inbound when the default profile port changes', () => {
    const initial = planDefaultSubscriptionPortSync(null, {
      inboundPort: 49362,
      profilePort: 49362,
    });
    const changed = planDefaultSubscriptionPortSync(initial.state, {
      inboundPort: 49362,
      profilePort: 1995,
    });

    expect(changed.setInboundPort).toBe(1995);
    expect(changed.setProfilePort).toBeUndefined();
  });

  it('repairs an existing divergent default profile from the inbound port', () => {
    const initial = planDefaultSubscriptionPortSync(null, {
      inboundPort: 52096,
      profilePort: 1995,
    });

    expect(initial.state.linked).toBe(true);
    expect(initial.setInboundPort).toBeUndefined();
    expect(initial.setProfilePort).toBe(52096);
    expect(initial.state.pending).toEqual({
      inboundPort: 52096,
      profilePort: 52096,
    });
  });

  it('uses the inbound as source of truth when both values reset at once', () => {
    const initial = planDefaultSubscriptionPortSync(null, {
      inboundPort: 49362,
      profilePort: 49362,
    });
    const changed = planDefaultSubscriptionPortSync(initial.state, {
      inboundPort: 52096,
      profilePort: 1995,
    });

    expect(changed.setInboundPort).toBeUndefined();
    expect(changed.setProfilePort).toBe(52096);
  });

  it('preserves linkage while an input is temporarily empty', () => {
    const initial = planDefaultSubscriptionPortSync(null, {
      inboundPort: 49362,
      profilePort: 49362,
    });
    const cleared = planDefaultSubscriptionPortSync(initial.state, {
      inboundPort: 49362,
      profilePort: null,
    });
    const typed = planDefaultSubscriptionPortSync(cleared.state, {
      inboundPort: 49362,
      profilePort: 1995,
    });

    expect(cleared.state.linked).toBe(true);
    expect(typed.setInboundPort).toBe(1995);
  });

  it('keeps the default profile linked after the repair is observed', () => {
    const initial = planDefaultSubscriptionPortSync(null, {
      inboundPort: 52096,
      profilePort: 1995,
    });
    const repaired = planDefaultSubscriptionPortSync(initial.state, {
      inboundPort: 52096,
      profilePort: 52096,
    });

    expect(repaired.state.linked).toBe(true);
    expect(repaired.state.pending).toBeNull();
  });


  it('sanitizes inactive XHTTP fields while preserving QUIC FinalMask', () => {
    const effective = effectiveSubscriptionProfileStream(
      baseStream(),
      {
        enabled: true,
        remark: 'xhttp-stream-one',
        dest: 'xhttp.example.com',
        port: 443,
        network: 'xhttp',
        security: 'same',
        forceTls: 'same',
        xhttpSettings: {
          host: '',
          path: '/',
          mode: 'stream-one',
          xPaddingBytes: '100-1000',
          xPaddingObfsMode: false,
          xPaddingKey: 'stale',
          xPaddingHeader: 'stale',
          xPaddingPlacement: 'header',
          xPaddingMethod: 'tokenish',
          sessionIDPlacement: 'path',
          sessionIDKey: 'stale',
          sessionIDTable: '',
          sessionIDLength: '8-16',
          seqPlacement: 'path',
          seqKey: 'stale',
          uplinkDataPlacement: 'header',
          uplinkDataKey: 'stale',
          scMaxEachPostBytes: '1024',
          scMaxBufferedPosts: 30,
          scStreamUpServerSecs: '20-80',
          serverMaxHeaderBytes: 0,
          uplinkHTTPMethod: 'GET',
          headers: {},
          scMinPostsIntervalMs: '50-150',
          uplinkChunkSize: 0,
          noGRPCHeader: false,
          noSSEHeader: true,
          enableXmux: false,
        },
        finalmask: {
          tcp: [],
          udp: [],
          quicParams: {
            congestion: 'bbr',
            maxIncomingStreams: 1024,
          },
        },
      },
      'xhttp.example.com',
    );

    expect(effective.network).toBe('xhttp');
    if (effective.network !== 'xhttp') throw new Error('expected xhttp');
    expect(effective.xhttpSettings.mode).toBe('stream-one');
    for (const key of [
      'scMaxEachPostBytes',
      'scMaxBufferedPosts',
      'scMinPostsIntervalMs',
      'scStreamUpServerSecs',
      'uplinkDataPlacement',
      'uplinkDataKey',
      'uplinkHTTPMethod',
      'xPaddingKey',
      'xPaddingHeader',
      'xPaddingPlacement',
      'xPaddingMethod',
      'sessionIDKey',
      'sessionIDLength',
      'seqKey',
      'enableXmux',
    ]) {
      expect(effective.xhttpSettings).not.toHaveProperty(key);
    }
    expect(effective.finalmask?.quicParams).toMatchObject({
      congestion: 'bbr',
      maxIncomingStreams: 1024,
    });
  });
});


describe('subscription profile editor protocol binding', () => {
  it('reads the protocol from the RHF inbound form', () => {
    const source = readFileSync(
      new URL(
        '../pages/inbounds/form/transport/subscription-profile-editor.tsx',
        import.meta.url,
      ),
      'utf8',
    );

    expect(source).toContain(
      "const { control } = useFormContext();",
    );
    expect(source).toContain(
      "useWatch({ control, name: 'protocol' })",
    );
    expect(source).not.toContain(
      "Form.useWatch('protocol', form)",
    );
  });
});


describe('mKCP profile-owned FinalMask', () => {
  it('detaches an explicit mKCP profile from the parent mask and defaults mkcp-legacy', () => {
    const parent: StreamSettings = {
      ...baseStream(),
      finalmask: {
        tcp: [{ type: 'sudoku', settings: {} }],
        udp: [],
      },
    };
    const effective = effectiveSubscriptionProfileStream(
      parent,
      {
        ...createSubscriptionProfileDraft(46237),
        network: 'kcp',
        security: 'none',
        kcpSettings: {
          mtu: 1350,
          tti: 20,
          uplinkCapacity: 5,
          downlinkCapacity: 20,
          cwndMultiplier: 1,
          maxSendingWindow: 2097152,
        },
      },
    );

    expect(effective.network).toBe('kcp');
    expect(effective.finalmask?.tcp).toEqual([]);
    expect(effective.finalmask?.udp).toEqual([{
      type: 'mkcp-legacy',
      settings: { header: '', value: '' },
    }]);
  });

  it('materializes the mKCP compatibility mask before save without touching same profiles', () => {
    const normalized = normalizeSubscriptionProfilesForSave([
      {
        ...createSubscriptionProfileDraft(46237),
        network: 'kcp',
      },
      {
        ...createSubscriptionProfileDraft(30856),
        network: 'same',
      },
    ]);

    expect(normalized[0].finalmask?.udp).toEqual([{
      type: 'mkcp-legacy',
      settings: { header: '', value: '' },
    }]);
    expect(normalized[1].finalmask).toBeUndefined();
  });

  it('clears a parent mask for an explicit non-mKCP transport override', () => {
    const parent: StreamSettings = {
      ...baseStream(),
      finalmask: {
        tcp: [{ type: 'sudoku', settings: {} }],
        udp: [],
      },
    };
    const effective = effectiveSubscriptionProfileStream(
      parent,
      {
        ...createSubscriptionProfileDraft(1995),
        network: 'grpc',
        grpcSettings: {
          serviceName: '',
          authority: '',
          multiMode: false,
        },
      },
    );
    expect(effective.finalmask).toBeUndefined();
  });
});
