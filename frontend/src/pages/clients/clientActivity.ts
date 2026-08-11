export interface ClientActivityStatus {
  clientId: number;
  enabled: boolean;
  generation: number;
  dataEpoch: number;
}

export interface ClientActivityItem {
  destination: string;
  sourceIp: string;
  uploadBytes: number;
  downloadBytes: number;
}

export interface ClientActivityList {
  enabled: boolean;
  generation: number;
  dataEpoch: number;
  items: ClientActivityItem[];
  total: number;
  page: number;
  pageSize: number;
}

function recordOf(value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return null;
  }

  return value as Record<string, unknown>;
}

function safeInteger(
  value: unknown,
  minimum: number,
): number | null {
  if (
    typeof value !== 'number'
    || !Number.isSafeInteger(value)
    || value < minimum
  ) {
    return null;
  }

  return value;
}

export function parseClientActivityStatus(
  value: unknown,
): ClientActivityStatus | null {
  const record = recordOf(value);
  if (!record || typeof record.enabled !== 'boolean') {
    return null;
  }

  const clientId = safeInteger(record.clientId, 1);
  const generation = safeInteger(record.generation, 0);
  const dataEpoch = safeInteger(record.dataEpoch, 1);

  if (
    clientId === null
    || generation === null
    || dataEpoch === null
  ) {
    return null;
  }

  return {
    clientId,
    enabled: record.enabled,
    generation,
    dataEpoch,
  };
}

function parseClientActivityItem(
  value: unknown,
): ClientActivityItem | null {
  const record = recordOf(value);

  if (
    !record
    || typeof record.destination !== 'string'
    || typeof record.sourceIp !== 'string'
  ) {
    return null;
  }

  const destination = record.destination.trim();
  const sourceIp = record.sourceIp.trim();
  const uploadBytes = safeInteger(record.uploadBytes, 0);
  const downloadBytes = safeInteger(record.downloadBytes, 0);

  if (
    !destination
    || !sourceIp
    || uploadBytes === null
    || downloadBytes === null
  ) {
    return null;
  }

  return {
    destination,
    sourceIp,
    uploadBytes,
    downloadBytes,
  };
}

export function parseClientActivityList(
  value: unknown,
): ClientActivityList | null {
  const record = recordOf(value);

  if (
    !record
    || typeof record.enabled !== 'boolean'
    || !Array.isArray(record.items)
  ) {
    return null;
  }

  const generation = safeInteger(record.generation, 0);
  const dataEpoch = safeInteger(record.dataEpoch, 1);
  const total = safeInteger(record.total, 0);
  const page = safeInteger(record.page, 1);
  const pageSize = safeInteger(record.pageSize, 1);

  if (
    generation === null
    || dataEpoch === null
    || total === null
    || page === null
    || pageSize === null
  ) {
    return null;
  }

  const items: ClientActivityItem[] = [];

  for (const rawItem of record.items) {
    const item = parseClientActivityItem(rawItem);
    if (item) items.push(item);
  }

  return {
    enabled: record.enabled,
    generation,
    dataEpoch,
    items,
    total,
    page,
    pageSize,
  };
}

export interface ClientActivityExport {
  enabled: boolean;
  generation: number;
  dataEpoch: number;
  items: ClientActivityItem[];
  total: number;
}

export function parseClientActivityExport(
  value: unknown,
): ClientActivityExport | null {
  const record = recordOf(value);

  if (
    !record
    || typeof record.enabled !== 'boolean'
    || !Array.isArray(record.items)
  ) {
    return null;
  }

  const generation = safeInteger(record.generation, 0);
  const dataEpoch = safeInteger(record.dataEpoch, 1);
  const total = safeInteger(record.total, 0);

  if (
    generation === null
    || dataEpoch === null
    || total === null
  ) {
    return null;
  }

  const items: ClientActivityItem[] = [];

  for (const rawItem of record.items) {
    const item = parseClientActivityItem(rawItem);

    if (!item) {
      return null;
    }

    items.push(item);
  }

  if (items.length !== total) {
    return null;
  }

  return {
    enabled: record.enabled,
    generation,
    dataEpoch,
    items,
    total,
  };
}
