// useMemories.ts owns the memory surface.
//
// It is a separate file from useThreads.ts because memories are scoped to the
// user, not to a thread: the same list is shown whatever conversation is open,
// and invalidating a thread must not touch it. Folding the two together would
// have made every thread-scoped refetch re-read a list that thread cannot change.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { chatApi } from '../api/chat'
import type { MemoryView } from '../api/types'

export const memoryKeys = {
  list: ['memories'] as const,
}

export function useMemories() {
  return useQuery({
    queryKey: memoryKeys.list,
    queryFn: () => chatApi.listMemories(),
    select: (data): MemoryView[] => data.memories,
  })
}

export function useUpdateMemory() {
  const client = useQueryClient()
  return useMutation({
    // The endpoint is a patch, not a replace: sending a field the user did not
    // touch would be a second write the server did not ask for, and it would
    // clobber whatever the agent had already stored there.
    mutationFn: (input: { memoryId: string; content?: string; memory_type?: string }) =>
      chatApi.updateMemory(input.memoryId, {
        ...(input.content !== undefined ? { content: input.content } : {}),
        ...(input.memory_type !== undefined ? { memory_type: input.memory_type } : {}),
      }),
    onSuccess: () => void client.invalidateQueries({ queryKey: memoryKeys.list }),
  })
}

export function useDeleteMemory() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (memoryId: string) => chatApi.deleteMemory(memoryId),
    onSuccess: () => void client.invalidateQueries({ queryKey: memoryKeys.list }),
  })
}
