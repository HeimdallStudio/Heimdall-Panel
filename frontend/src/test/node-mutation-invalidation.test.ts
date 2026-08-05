import type { QueryClient } from '@tanstack/react-query';
import { describe, expect, it, vi } from 'vitest';

import { keys } from '@/api/queryKeys';
import { invalidateNodeMutationQueries } from '@/api/queries/useNodeMutations';

describe('node mutation cache invalidation', () => {
  it('invalidates the complete inbound query family after node changes', () => {
    const invalidateQueries = vi.fn();
    const queryClient = {
      invalidateQueries,
    } as unknown as Pick<QueryClient, 'invalidateQueries'>;

    invalidateNodeMutationQueries(queryClient);

    expect(invalidateQueries).toHaveBeenCalledWith({
      queryKey: keys.nodes.root(),
    });
    expect(invalidateQueries).toHaveBeenCalledWith({
      queryKey: keys.inbounds.root(),
    });
  });
});
