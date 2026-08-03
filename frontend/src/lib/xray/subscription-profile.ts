import type { StreamSettings } from '@/schemas/api/inbound';
import type { ExternalProxyEntry } from '@/schemas/protocols/stream/external-proxy';
import type { FinalMaskStreamSettings } from '@/schemas/protocols/stream/finalmask';
import {
  PROFILE_TRANSPORT_SETTING_KEYS,
  createProfileTransportDefaults,
  profileTransportSettingsKey,
} from '@/lib/xray/forms/transport/subscription-profile-transport';
import { sanitizeXhttpSettings } from '@/lib/xray/forms/transport/xhttp-foundation';

const AUTOMATIC_RUNTIME_TOPOLOGY_FIELDS = new Set([
  'enabled',
  'mode',
  'listen',
  'port',
]);
const MODERN_SUBSCRIPTION_PROFILE_FIELDS = [
  'network',
  'security',
  'tlsSettings',
  'realitySettings',
  'sockopt',
  'mux',
  'finalmask',
  'overrideSniFromAddress',
  'keepSniBlank',
  'verifyPeerCertByName',
  'flow',
  'runtime',
  'tcpSettings',
  'kcpSettings',
  'wsSettings',
  'grpcSettings',
  'httpupgradeSettings',
  'xhttpSettings',
] as const;

export const DEFAULT_SUBSCRIPTION_PROFILE_PORT = 1995;

// Keep this list aligned with profilevalidation.runtimeProtocolSupported and
// service.runtimeProfileProtocolSupported. Protocols outside this set cannot
// own automatic Multi Profile runtime listeners.
const SUBSCRIPTION_PROFILE_PROTOCOLS = new Set([
  'vless',
  'vmess',
  'trojan',
  'shadowsocks',
  'hysteria',
]);

export function supportsSubscriptionProfiles(protocol: string): boolean {
  return SUBSCRIPTION_PROFILE_PROTOCOLS.has(protocol.trim().toLowerCase());
}

export function isModernSubscriptionProfile(
  profile: ExternalProxyEntry,
): boolean {
  const record = profile as ExternalProxyEntry & Record<string, unknown>;
  return MODERN_SUBSCRIPTION_PROFILE_FIELDS.some((field) => (
    Object.prototype.hasOwnProperty.call(record, field)
  ));
}

export function hasAutomaticRuntimeMarker(profile: ExternalProxyEntry): boolean {
  return Object.prototype.hasOwnProperty.call(
    profile as ExternalProxyEntry & Record<string, unknown>,
    'runtime',
  );
}

function hasMalformedRuntimeMetadata(profile: ExternalProxyEntry): boolean {
  const raw = (profile as ExternalProxyEntry & Record<string, unknown>).runtime;
  return raw != null && (typeof raw !== 'object' || Array.isArray(raw));
}

function normalizedRuntimeMetadata(
  profile: ExternalProxyEntry,
): Record<string, unknown> {
  const record = profile as ExternalProxyEntry & Record<string, unknown>;
  const raw = record.runtime;
  if (raw == null || typeof raw !== 'object' || Array.isArray(raw)) return {};

  return Object.fromEntries(
    Object.entries(raw).filter(([key, value]) => (
      !AUTOMATIC_RUNTIME_TOPOLOGY_FIELDS.has(key) && value !== undefined
    )),
  );
}

export function duplicateSubscriptionProfile(
  profile: ExternalProxyEntry,
): ExternalProxyEntry {
  const duplicate = jsonClone(profile);
  if (!isModernSubscriptionProfile(duplicate)) return duplicate;
  if (hasMalformedRuntimeMetadata(duplicate)) return duplicate;

  const preserveRuntimeMarker = hasAutomaticRuntimeMarker(duplicate);
  const runtime = normalizedRuntimeMetadata(duplicate);
  delete runtime.id;
  if (Object.keys(runtime).length > 0 || preserveRuntimeMarker) {
    duplicate.runtime = runtime as ExternalProxyEntry['runtime'];
  } else {
    delete duplicate.runtime;
  }
  return duplicate;
}

export function createSubscriptionProfileDraft(
  defaultPort = DEFAULT_SUBSCRIPTION_PROFILE_PORT,
): ExternalProxyEntry {
  const safePort = Number.isInteger(defaultPort) && defaultPort > 0 && defaultPort <= 65535
    ? defaultPort
    : DEFAULT_SUBSCRIPTION_PROFILE_PORT;

  return {
    enabled: true,
    remark: '',
    // Blank intentionally inherits the address resolved from the inbound's
    // share-address strategy. A user-entered value remains an explicit override.
    dest: '',
    port: safePort,
    network: 'same',
    security: 'same',
    forceTls: 'same',
    overrideSniFromAddress: false,
    keepSniBlank: false,
    excludeFromSubTypes: [],
    mihomoX25519: false,
    shuffleHost: false,
    // Hidden ownership marker. Backend assigns the stable runtime.id and owns
    // every listener decision; older structured rows without this marker stay
    // subscription-only for migration safety.
    runtime: {},
  };
}

export function normalizeSubscriptionProfilesForSave(
  profiles: ExternalProxyEntry[],
): ExternalProxyEntry[] {
  return profiles.map((profile) => {
    const record = profile as ExternalProxyEntry & Record<string, unknown>;
    const network = typeof profile.network === 'string'
      ? profile.network.trim().toLowerCase()
      : '';
    let normalized = profile;
    if (
      (network === 'kcp' || network === 'mkcp')
      && !Object.prototype.hasOwnProperty.call(record, 'finalmask')
    ) {
      normalized = {
        ...normalized,
        finalmask: createMKCPLegacyFinalMask(),
      };
    }

    if (normalized.enabled === false || !isModernSubscriptionProfile(normalized)) {
      return normalized;
    }
    if (hasMalformedRuntimeMetadata(normalized)) return normalized;

    const preserveRuntimeMarker = hasAutomaticRuntimeMarker(normalized);
    const runtime = normalizedRuntimeMetadata(normalized);
    if (typeof runtime.id === 'string') {
      const trimmed = runtime.id.trim();
      if (trimmed) runtime.id = trimmed;
      else delete runtime.id;
    }

    if (Object.keys(runtime).length > 0 || preserveRuntimeMarker) {
      return { ...normalized, runtime } as ExternalProxyEntry;
    }
    const withoutRuntime = { ...normalized } as ExternalProxyEntry & {
      runtime?: unknown;
    };
    delete withoutRuntime.runtime;
    return withoutRuntime;
  });
}

export function normalizeSubscriptionProfilesForProtocolSave(
  protocol: string,
  profiles: ExternalProxyEntry[],
): ExternalProxyEntry[] {
  if (!supportsSubscriptionProfiles(protocol)) return [];
  return normalizeSubscriptionProfilesForSave(profiles);
}

export interface SubscriptionProfileEndpoint {
  address: string;
  port: number;
  remark: string;
  streamSettings: StreamSettings;
  profile: ExternalProxyEntry | null;
}

const TRANSPORT_KEYS = [
  ...PROFILE_TRANSPORT_SETTING_KEYS,
  'hysteriaSettings',
] as const;

type MutableStream = Record<string, unknown>;
type MutableProfile = ExternalProxyEntry & Record<string, unknown>;

function jsonClone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function explicitProfileNetwork(profile: ExternalProxyEntry): string | null {
  const network = typeof profile.network === 'string'
    ? profile.network.trim().toLowerCase()
    : '';
  if (!network || network === 'same') return null;
  return network === 'mkcp' ? 'kcp' : network;
}

export function createMKCPLegacyFinalMask(): FinalMaskStreamSettings {
  return {
    tcp: [],
    udp: [{
      type: 'mkcp-legacy',
      settings: {
        header: '',
        value: '',
      },
    }],
  };
}

export function isOnlyDefaultMKCPLegacyFinalMask(value: unknown): boolean {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const record = value as Record<string, unknown>;
  const tcp = Array.isArray(record.tcp) ? record.tcp : [];
  const udp = Array.isArray(record.udp) ? record.udp : [];
  if (tcp.length > 0 || record.quicParams != null || udp.length !== 1) return false;
  const mask = udp[0];
  if (!mask || typeof mask !== 'object' || Array.isArray(mask)) return false;
  const maskRecord = mask as Record<string, unknown>;
  if (maskRecord.type !== 'mkcp-legacy') return false;
  const settings = maskRecord.settings;
  if (settings == null) return true;
  if (typeof settings !== 'object' || Array.isArray(settings)) return false;
  const settingRecord = settings as Record<string, unknown>;
  return (settingRecord.header ?? '') === ''
    && (settingRecord.value ?? '') === ''
    && Object.keys(settingRecord).every((key) => key === 'header' || key === 'value');
}

export function effectiveSubscriptionProfileFinalMask(
  base: Partial<StreamSettings>,
  profile: ExternalProxyEntry,
  selectedNetwork: string,
): FinalMaskStreamSettings | undefined {
  const record = profile as ExternalProxyEntry & Record<string, unknown>;
  const ownsTransport = explicitProfileNetwork(profile) !== null;

  if (Object.prototype.hasOwnProperty.call(record, 'finalmask')) {
    const value = record.finalmask;
    if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined;
    return jsonClone(value as FinalMaskStreamSettings);
  }
  if (ownsTransport && selectedNetwork === 'kcp') {
    return createMKCPLegacyFinalMask();
  }
  if (ownsTransport) return undefined;
  return base.finalmask ? jsonClone(base.finalmask) : undefined;
}

function transportKey(network: string): (typeof TRANSPORT_KEYS)[number] | null {
  if (network === 'hysteria') return 'hysteriaSettings';
  return profileTransportSettingsKey(network);
}

function defaultTransportSettings(network: string): Record<string, unknown> {
  switch (network) {
    case 'tcp':
    case 'ws':
    case 'grpc':
    case 'httpupgrade':
    case 'xhttp':
      return createProfileTransportDefaults(network);
    default:
      return {};
  }
}

function defaultTlsSettings(): Record<string, unknown> {
  return {
    serverName: '',
    alpn: [],
    settings: {
      fingerprint: 'chrome',
      echConfigList: '',
      pinnedPeerCertSha256: [],
      verifyPeerCertByName: '',
      allowInsecure: false,
    },
  };
}

function defaultRealitySettings(): Record<string, unknown> {
  return {
    serverNames: [],
    shortIds: [],
    settings: {
      publicKey: '',
      fingerprint: 'chrome',
      serverName: '',
      shortId: '',
      spiderX: '/',
      mldsa65Verify: '',
    },
  };
}

function applyLegacyTlsFields(profile: MutableProfile, stream: MutableStream): void {
  const tls = (stream.tlsSettings && typeof stream.tlsSettings === 'object')
    ? stream.tlsSettings as Record<string, unknown>
    : defaultTlsSettings();
  stream.tlsSettings = tls;

  if (profile.sni) tls.serverName = profile.sni;
  if (Array.isArray(profile.alpn) && profile.alpn.length > 0) tls.alpn = [...profile.alpn];

  const settings = (tls.settings && typeof tls.settings === 'object')
    ? tls.settings as Record<string, unknown>
    : {};
  tls.settings = settings;
  if (profile.fingerprint) settings.fingerprint = profile.fingerprint;
  if (Array.isArray(profile.pinnedPeerCertSha256) && profile.pinnedPeerCertSha256.length > 0) {
    settings.pinnedPeerCertSha256 = [...profile.pinnedPeerCertSha256];
  }
  if (profile.echConfigList) settings.echConfigList = profile.echConfigList;
  if (profile.verifyPeerCertByName) {
    settings.verifyPeerCertByName = profile.verifyPeerCertByName;
  }
  if (typeof profile.allowInsecure === 'boolean') settings.allowInsecure = profile.allowInsecure;
}

function applySubscriptionProfileSniMode(
  profile: ExternalProxyEntry,
  stream: MutableStream,
  resolvedAddress: string,
): void {
  if (stream.security !== 'tls') return;

  const tlsSettings = stream.tlsSettings as
    | Record<string, unknown>
    | undefined;

  if (!tlsSettings) return;

  if (profile.keepSniBlank) {
    tlsSettings.serverName = '';
    return;
  }

  if (profile.overrideSniFromAddress) {
    tlsSettings.serverName = resolvedAddress.trim();
  }
}

export function effectiveSubscriptionProfileStream(
  base: StreamSettings,
  profile: ExternalProxyEntry,
  resolvedAddress = '',
): StreamSettings {
  const stream = jsonClone(base) as unknown as MutableStream;
  delete stream.externalProxy;
  const mutableProfile = profile as MutableProfile;

  const baseNetwork = typeof stream.network === 'string' ? stream.network : '';
  const selectedNetwork = profile.network && profile.network !== 'same'
    ? profile.network
    : baseNetwork;
  const selectedTransportKey = transportKey(selectedNetwork);
  if (selectedTransportKey) {
    if (selectedNetwork !== baseNetwork) {
      for (const key of TRANSPORT_KEYS) delete stream[key];
      stream.network = selectedNetwork;
      stream[selectedTransportKey] = defaultTransportSettings(selectedNetwork);
    }
    const profileSettings = mutableProfile[selectedTransportKey];
    if (profileSettings && typeof profileSettings === 'object') {
      const clonedSettings = jsonClone(profileSettings) as Record<string, unknown>;
      stream[selectedTransportKey] = selectedNetwork === 'xhttp'
        ? sanitizeXhttpSettings(clonedSettings)
        : clonedSettings;
    } else if (!stream[selectedTransportKey]) {
      stream[selectedTransportKey] = defaultTransportSettings(selectedNetwork);
    }
  }

  let security = profile.security;
  if (!security || security === 'same') security = profile.forceTls;
  if (!security || security === 'same') {
    security = typeof stream.security === 'string' ? stream.security as 'none' | 'tls' | 'reality' : 'none';
  }

  switch (security) {
    case 'none':
      stream.security = 'none';
      delete stream.tlsSettings;
      delete stream.realitySettings;
      break;
    case 'tls':
      stream.security = 'tls';
      delete stream.realitySettings;
      if (profile.tlsSettings) stream.tlsSettings = jsonClone(profile.tlsSettings);
      else if (!stream.tlsSettings) stream.tlsSettings = defaultTlsSettings();
      applyLegacyTlsFields(mutableProfile, stream);
      break;
    case 'reality':
      stream.security = 'reality';
      delete stream.tlsSettings;
      if (profile.realitySettings) stream.realitySettings = jsonClone(profile.realitySettings);
      else if (!stream.realitySettings) stream.realitySettings = defaultRealitySettings();
      break;
    default:
      break;
  }

  applySubscriptionProfileSniMode(
    profile,
    stream,
    resolvedAddress,
  );

  if (profile.mux) stream.mux = jsonClone(profile.mux);
  if (profile.sockopt) stream.sockopt = jsonClone(profile.sockopt);

  const effectiveFinalMask = effectiveSubscriptionProfileFinalMask(
    base,
    profile,
    selectedNetwork,
  );
  if (effectiveFinalMask) stream.finalmask = effectiveFinalMask;
  else delete stream.finalmask;

  return stream as unknown as StreamSettings;
}

export function expandSubscriptionProfileEndpoints(
  streamSettings: StreamSettings,
  defaultAddress: string,
  defaultPort: number,
): SubscriptionProfileEndpoint[] {
  const profiles = streamSettings.externalProxy;
  if (!profiles || profiles.length === 0) {
    const stream = jsonClone(streamSettings) as unknown as MutableStream;
    delete stream.externalProxy;
    return [{
      address: defaultAddress,
      port: defaultPort,
      remark: '',
      streamSettings: stream as unknown as StreamSettings,
      profile: null,
    }];
  }

  return profiles
    .filter((profile) => profile.enabled !== false)
    .map((profile) => ({
      address: profile.dest?.trim() || defaultAddress,
      port: Number.isInteger(profile.port) && profile.port > 0 && profile.port <= 65535
        ? profile.port
        : defaultPort,
      remark: profile.remark ?? '',
      streamSettings: effectiveSubscriptionProfileStream(
        streamSettings,
        profile,
        profile.dest?.trim() || defaultAddress,
      ),
      profile,
    }));
}

export interface DefaultSubscriptionPortPair {
  inboundPort: number | null;
  profilePort: number | null;
}

export interface DefaultSubscriptionPortSyncState
  extends DefaultSubscriptionPortPair {
  linked: boolean;
  pending: DefaultSubscriptionPortPair | null;
}

export interface DefaultSubscriptionPortSyncPlan {
  state: DefaultSubscriptionPortSyncState;
  setInboundPort?: number;
  setProfilePort?: number;
}

export function normalizeSubscriptionPort(value: unknown): number | null {
  if (
    typeof value !== 'number'
    || !Number.isInteger(value)
    || value < 1
    || value > 65535
  ) {
    return null;
  }
  return value;
}

function portPairsEqual(
  left: DefaultSubscriptionPortPair,
  right: DefaultSubscriptionPortPair,
): boolean {
  return left.inboundPort === right.inboundPort
    && left.profilePort === right.profilePort;
}

function createPortSyncState(
  pair: DefaultSubscriptionPortPair,
): DefaultSubscriptionPortSyncState {
  return {
    ...pair,
    linked: true,
    pending: null,
  };
}

function planProfilePortFromInbound(
  inboundPort: number,
): DefaultSubscriptionPortSyncPlan {
  const target = {
    inboundPort,
    profilePort: inboundPort,
  };

  return {
    state: {
      ...target,
      linked: true,
      pending: target,
    },
    setProfilePort: inboundPort,
  };
}

// Profile zero is the default endpoint and is permanently linked to the
// inbound port. On initialization/import the inbound is the source of truth,
// which also repairs profiles created by builds that incorrectly seeded 1995.
// Once mounted, editing either side keeps both values synchronized.
export function planDefaultSubscriptionPortSync(
  previous: DefaultSubscriptionPortSyncState | null,
  current: DefaultSubscriptionPortPair,
): DefaultSubscriptionPortSyncPlan {
  if (previous === null) {
    if (
      current.inboundPort !== null
      && current.profilePort !== current.inboundPort
    ) {
      return planProfilePortFromInbound(current.inboundPort);
    }
    return { state: createPortSyncState(current) };
  }

  if (previous.pending !== null) {
    if (portPairsEqual(current, previous.pending)) {
      return {
        state: {
          ...current,
          linked: true,
          pending: null,
        },
      };
    }

    // setFieldValue can produce an intermediate render. Do not reverse the
    // synchronization while waiting for the target field to update.
    return { state: previous };
  }

  const inboundChanged = current.inboundPort !== previous.inboundPort;
  const profileChanged = current.profilePort !== previous.profilePort;

  if (!inboundChanged && !profileChanged) {
    return { state: previous };
  }

  if (inboundChanged && !profileChanged) {
    if (current.inboundPort === null) {
      return { state: previous };
    }
    return planProfilePortFromInbound(current.inboundPort);
  }

  if (profileChanged && !inboundChanged) {
    if (current.profilePort === null) {
      return { state: previous };
    }

    const target = {
      inboundPort: current.profilePort,
      profilePort: current.profilePort,
    };

    return {
      state: {
        ...target,
        linked: true,
        pending: target,
      },
      setInboundPort: current.profilePort,
    };
  }

  // A form reset/import can replace both values in one render. The inbound is
  // authoritative at that boundary so stale profile data cannot rewrite it.
  if (current.inboundPort !== null) {
    if (current.profilePort === current.inboundPort) {
      return { state: createPortSyncState(current) };
    }
    return planProfilePortFromInbound(current.inboundPort);
  }

  return { state: previous };
}
