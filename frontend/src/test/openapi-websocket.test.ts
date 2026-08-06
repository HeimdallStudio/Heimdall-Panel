import { describe, expect, it } from 'vitest';

import openApiDocument from '../../public/openapi.json';

interface Operation {
  security?: Array<Record<string, unknown[]>>;
  responses?: Record<string, { description?: string; content?: unknown }>;
}

interface WebSocketEvent {
  channel: string;
  type: string;
  summary: string;
  example: {
    type: string;
    payload: unknown;
    time: number;
  };
}

interface OpenApiDocument {
  paths: Record<string, Record<string, Operation>>;
  'x-websocket-events': WebSocketEvent[];
}

const document = openApiDocument as unknown as OpenApiDocument;
const allowedPathItemKeys = new Set([
  'get',
  'post',
  'put',
  'patch',
  'delete',
  'head',
  'options',
  'trace',
  'parameters',
  'summary',
  'description',
  'servers',
]);

describe('generated OpenAPI WebSocket metadata', () => {
  it('contains only standard OpenAPI path-item keys', () => {
    const invalid = Object.entries(document.paths).flatMap(([path, item]) =>
      Object.keys(item)
        .filter((key) => !allowedPathItemKeys.has(key))
        .map((key) => `${path}:${key}`),
    );

    expect(invalid).toEqual([]);
  });

  it('documents the session-only WebSocket handshake as HTTP 101', () => {
    const operation = document.paths['/ws']?.get;
    expect(operation).toBeTruthy();
    expect(operation?.security).toEqual([{ cookieAuth: [] }]);
    expect(operation?.responses).toHaveProperty('101');
    expect(operation?.responses?.['101']?.description).toBe('Switching Protocols');
    expect(operation?.responses?.['101']).not.toHaveProperty('content');
    expect(operation?.responses).not.toHaveProperty('200');
    expect(operation?.responses?.['401']).not.toHaveProperty('content');
    expect(operation?.responses?.['403']).not.toHaveProperty('content');
  });

  it('preserves the documented events in a vendor extension', () => {
    const events = document['x-websocket-events'];
    expect(events.map((event) => event.type)).toEqual([
      'status',
      'xray_state',
      'notification',
      'presence',
      'invalidate',
    ]);

    for (const event of events) {
      expect(event.channel).toBe('/ws');
      expect(event.summary.length).toBeGreaterThan(0);
      expect(event.example.type).toBe(event.type);
      expect(event.example).toHaveProperty('payload');
      expect(event.example.time).toBeTypeOf('number');
    }
  });

  it('matches the concrete notification, Xray, presence, and invalidate payload keys', () => {
    const byType = Object.fromEntries(
      document['x-websocket-events'].map((event) => [event.type, event.example]),
    );

    expect(byType.notification.payload).toEqual({
      title: 'Xray service restarted',
      message: 'Xray has been restarted successfully',
      level: 'success',
    });
    expect(byType.xray_state.payload).toEqual({
      state: 'running',
      errorMsg: '',
    });
    expect(byType.presence.payload).toEqual({
      onlineClients: ['alice@example.test'],
    });
    expect(byType.invalidate.payload).toEqual({
      type: 'inbounds',
    });
  });
});
