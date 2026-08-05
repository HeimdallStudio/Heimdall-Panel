import { describe, expect, it } from 'vitest';

import { createNodeFormDefaultValues, NodeFormSchema } from '@/schemas/node';

describe('node inbound import defaults', () => {
  it('creates a new node with selected mode and an empty import list', () => {
    const defaults = createNodeFormDefaultValues();

    expect(defaults.inboundSyncMode).toBe('selected');
    expect(defaults.inboundTags).toEqual([]);
  });

  it('defaults omitted API form values to selected mode', () => {
    const parsed = NodeFormSchema.parse({
      id: 0,
      name: 'node-a',
      remark: '',
      scheme: 'https',
      address: 'node.example.com',
      port: 2053,
      basePath: '/',
      apiToken: 'token',
      enable: true,
      allowPrivateAddress: false,
      tlsVerifyMode: 'verify',
      pinnedCertSha256: '',
      inboundTags: null,
      outboundTag: '',
    });

    expect(parsed.inboundSyncMode).toBe('selected');
    expect(parsed.inboundTags).toEqual([]);
  });

  it('keeps all mode only when explicitly selected', () => {
    const parsed = NodeFormSchema.parse({
      ...createNodeFormDefaultValues(),
      name: 'node-a',
      address: 'node.example.com',
      apiToken: 'token',
      inboundSyncMode: 'all',
    });

    expect(parsed.inboundSyncMode).toBe('all');
  });
});
