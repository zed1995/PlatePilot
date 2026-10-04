// useThreads.ts holds the thread-scoped reads and the one write the console
// makes outside a turn.
//
// Query keys live in one object because the "turn finished" handler has to
// invalidate exactly what the stream just made stale: the transcript, the
// thread's own state badge, and the list's ordering. Scattering literals across
// the page is how one of those three ends up not being refreshed.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { chatApi } from '../api/chat'
import type { CandidateView, MessageView, RunView, Thread } from '../api/types'

export const threadKeys = {
  list: ['threads'] as const,
  detail: (threadId: string) => ['thread', threadId] as const,
  messages: (threadId: string) => ['thread-messages', threadId] as const,
  runs: (threadId: string) => ['runs', threadId] as const,
  run: (runId: string) => ['run', runId] as const,
  candidates: (threadId: string) => ['candidates', threadId] as const,
}

// listLimit is the page the sidebar shows. A demo user has a handful of threads;
// pagination is available on the API but is not worth a control here.
const listLimit = 50

// transcriptLimit is the visible tail of a thread. A conversation longer than
// this scrolls off rather than paging, which is the honest trade for a console
// whose threads are minutes long.
const transcriptLimit = 100

// runLimit is how much run history the replay panel lists.
const runLimit = 30

export function useThreads() {
  return useQuery({
    queryKey: threadKeys.list,
    queryFn: () => chatApi.listThreads({ limit: listLimit }),
    select: (data) => data.conversations,
  })
}

export function useThread(threadId: string | null) {
  return useQuery({
    queryKey: threadKeys.detail(threadId ?? ''),
    queryFn: () => chatApi.getThread(threadId as string),
    enabled: Boolean(threadId),
  })
}

export function useMessages(threadId: string | null) {
  return useQuery({
    queryKey: threadKeys.messages(threadId ?? ''),
    queryFn: () => chatApi.listMessages(threadId as string, { limit: transcriptLimit }),
    enabled: Boolean(threadId),
    select: (data): MessageView[] => data.messages,
  })
}

// useRuns lists a thread's runs, newest first, and useRun opens one.
//
// The console reads the run detail twice over: once to fill in the tool
// summaries the live stream never carried, and once to render the replay panel
// when a historical run is picked. One query keyed by run id serves both, so
// opening a row that is already the current run costs nothing.
export function useRuns(threadId: string | null) {
  return useQuery({
    queryKey: threadKeys.runs(threadId ?? ''),
    queryFn: () => chatApi.listRuns(threadId as string, { limit: runLimit }),
    enabled: Boolean(threadId),
    select: (data): RunView[] => data.runs,
  })
}

export function useRun(runId: string | null) {
  return useQuery({
    queryKey: threadKeys.run(runId ?? ''),
    queryFn: () => chatApi.getRun(runId as string),
    enabled: Boolean(runId),
  })
}

export function useCandidates(threadId: string | null) {
  return useQuery({
    queryKey: threadKeys.candidates(threadId ?? ''),
    queryFn: () => chatApi.listCandidates(threadId as string),
    enabled: Boolean(threadId),
    select: (data): CandidateView[] => data.candidates,
  })
}

export function useCreateThread() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (title?: string) => chatApi.createThread(title),
    onSuccess: (thread: Thread) => {
      // The new thread is written into the list cache before the refetch lands
      // so the sidebar never shows a gap between "created" and "listed".
      client.setQueryData<Thread[]>(threadKeys.list, (current) =>
        current ? [thread, ...current.filter((t) => t.thread_id !== thread.thread_id)] : [thread],
      )
      void client.invalidateQueries({ queryKey: threadKeys.list })
    },
  })
}

// useTurnWriter is the invalidation half of a finished turn.
//
// A turn writes the transcript, the thread's state and the candidate snapshot
// as side effects the client never sees directly, so the only correct response
// is to re-read all three. It is called once per turn rather than on every
// event: refetching mid-stream would fight the text arriving over the stream.
export function useTurnWriter() {
  const client = useQueryClient()
  return (threadId: string) => {
    void client.invalidateQueries({ queryKey: threadKeys.messages(threadId) })
    void client.invalidateQueries({ queryKey: threadKeys.detail(threadId) })
    void client.invalidateQueries({ queryKey: threadKeys.list })
    // The candidate snapshot and the run row are written as side effects of the
    // same turn; a replay panel that still shows the previous run would look
    // like the turn produced nothing.
    void client.invalidateQueries({ queryKey: threadKeys.candidates(threadId) })
    void client.invalidateQueries({ queryKey: threadKeys.runs(threadId) })
  }
}
