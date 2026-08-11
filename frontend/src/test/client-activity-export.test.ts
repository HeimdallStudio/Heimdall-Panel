import { describe, expect, it } from 'vitest';

import { parseClientActivityExport } from '@/pages/clients/clientActivity';

describe('Client Activity full export parsing', () => {
  it('accepts exports larger than the normal Activity API page cap', () => {
    const items = Array.from({ length: 450 }, (_, index) => ({
      destination: `destination-${index}.example`,
      sourceIp: `203.0.${Math.floor(index / 250)}.${(index % 250) + 1}`,
      uploadBytes: index + 1,
      downloadBytes: (index + 1) * 2,
    }));

    const parsed = parseClientActivityExport({
      enabled: true,
      generation: 11,
      dataEpoch: 7,
      total: items.length,
      items,
    });

    expect(parsed).not.toBeNull();
    expect(parsed?.enabled).toBe(true);
    expect(parsed?.generation).toBe(11);
    expect(parsed?.dataEpoch).toBe(7);
    expect(parsed?.total).toBe(450);
    expect(parsed?.items).toHaveLength(450);

    expect(parsed?.items[0]?.destination).toBe(
      'destination-0.example',
    );

    expect(parsed?.items[449]?.destination).toBe(
      'destination-449.example',
    );
  });

  it('does not silently truncate a large export response', () => {
    const items = Array.from({ length: 5000 }, (_, index) => ({
      destination: `scale-${index}.example`,
      sourceIp: `198.51.${Math.floor(index / 250)}.${(index % 250) + 1}`,
      uploadBytes: index,
      downloadBytes: index * 2,
    }));

    const parsed = parseClientActivityExport({
      enabled: true,
      generation: 3,
      dataEpoch: 9,
      total: 5000,
      items,
    });

    expect(parsed).not.toBeNull();
    expect(parsed?.total).toBe(5000);
    expect(parsed?.items).toHaveLength(5000);
    expect(parsed?.items[4999]?.destination).toBe(
      'scale-4999.example',
    );
  });

  it('rejects the whole export if even one record is invalid', () => {
    const parsed = parseClientActivityExport({
      enabled: true,
      generation: 3,
      dataEpoch: 9,
      total: 3,
      items: [
        {
          destination: 'one.example',
          sourceIp: '203.0.113.1',
          uploadBytes: 10,
          downloadBytes: 20,
        },
        {
          destination: '',
          sourceIp: '203.0.113.2',
          uploadBytes: 30,
          downloadBytes: 40,
        },
        {
          destination: 'three.example',
          sourceIp: '203.0.113.3',
          uploadBytes: 50,
          downloadBytes: 60,
        },
      ],
    });

    expect(parsed).toBeNull();
  });

  it('rejects an export when total does not match the returned records', () => {
    const parsed = parseClientActivityExport({
      enabled: true,
      generation: 3,
      dataEpoch: 9,
      total: 3,
      items: [
        {
          destination: 'one.example',
          sourceIp: '203.0.113.1',
          uploadBytes: 10,
          downloadBytes: 20,
        },
        {
          destination: 'two.example',
          sourceIp: '203.0.113.2',
          uploadBytes: 30,
          downloadBytes: 40,
        },
      ],
    });

    expect(parsed).toBeNull();
  });
});
