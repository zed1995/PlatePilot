// AgentConsole is the agent verification bench.
//
// It is a second, deliberate departure from "read-only admin console"
// (web/AGENTS.md): this page talks to /v1 as a person and does write. That is
// why it lives on its own route with its own sidebar group rather than being
// folded into the operator pages — the moment a page can send a message, the
// word "read-only" stops describing the app.
//
// Three columns, one idea: the thread list is what exists, the transcript is
// what was said, and the rail is what happened behind it.
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Wrench, Quote, SlidersHorizontal } from 'lucide-react'

import { chatApi, getUserId, setUserId } from '../api/chat'
import type { ConfirmResponse, MessageView } from '../api/types'
import { useAgentTurn, unresolvedCitations, type TurnState } from '../hooks/useAgentTurn'
import {
  useCandidates,
  useCreateThread,
  useMessages,
  useRun,
  useRuns,
  useThread,
  useThreads,
  useTurnWriter,
} from '../hooks/useThreads'
import { PageHeader } from '../components/page-header'
import { EmptyState } from '../components/empty-state'
import { ErrorState } from '../components/error-state'
import { Input } from '../components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../components/ui/tabs'
import { CandidateCards } from '../components/agent/candidate-cards'
import { CitationDrawer } from '../components/agent/citation-drawer'
import { Composer } from '../components/agent/composer'
import { GateBanner } from '../components/agent/gate-banner'
import { MemoryPanel } from '../components/agent/memory-panel'
import { MessageList } from '../components/agent/message-list'
import { RunReplayPanel } from '../components/agent/run-replay-panel'
import { SlotProbeCard } from '../components/agent/slot-probe-card'
import { ThreadList } from '../components/agent/thread-list'
import { ToolTimeline, mergeToolCalls } from '../components/agent/tool-timeline'
import { TurnStatusBar } from '../components/agent/turn-status-bar'

export const tabTools = 'tools'
export const tabCitations = 'citations'
export const tabUtilities = 'utilities'

// withoutEchoedTurn drops the messages the stream is already showing.
//
// A finished turn exists twice: once as the events this page reduced, and once
// as rows the server wrote. Rendering both would show the question and its
// answer twice over. Matching on content is deliberate — the turn has no
// message id until the transcript is re-read, and the assistant text the server
// persists is the text that was streamed.
export function withoutEchoedTurn(
  messages: MessageView[],
  prompt: string | null,
  turn: TurnState,
): MessageView[] {
  let out = messages
  if (turn.text) {
    const last = out[out.length - 1]
    if (last?.role === 'assistant' && last.content === turn.text) out = out.slice(0, -1)
  }
  if (prompt) {
    const last = out[out.length - 1]
    if (last?.role === 'user' && last.content === prompt) out = out.slice(0, -1)
  }
  return out
}

// citableIds is every document the visible transcript is allowed to cite.
//
// It is the union over the messages on screen rather than one turn's set,
// because a footnote has to stay checkable after the turn that produced it has
// scrolled into history. The per-bubble check still uses that message's own
// `evidence_ids`, which is the narrower and correct set for the red line about
// citations never escaping their turn.
export function citableIds(messages: MessageView[], turn: TurnState): number[] {
  const ids = new Set<number>(turn.citations)
  for (const message of messages) {
    for (const id of message.evidence_ids ?? []) ids.add(id)
  }
  return [...ids].sort((a, b) => a - b)
}

export default function AgentConsole() {
  const client = useQueryClient()
  const [identity, setIdentity] = useState(getUserId())
  const [activeId, setActiveId] = useState<string | null>(null)
  const [prompt, setPrompt] = useState<string | null>(null)
  const [tab, setTab] = useState(tabTools)
  const [selectedRunId, setSelectedRunId] = useState<string | null>(null)
  const [citationId, setCitationId] = useState<number | null>(null)
  const [confirmOutcome, setConfirmOutcome] = useState<ConfirmResponse | null>(null)
  const [memoryToken, setMemoryToken] = useState(0)

  const threads = useThreads()
  const createThread = useCreateThread()
  const thread = useThread(activeId)
  const messages = useMessages(activeId)
  const runs = useRuns(activeId)
  const candidates = useCandidates(activeId)
  const writeTurn = useTurnWriter()

  const turn = useAgentTurn({
    onFinished: (finished) => {
      // The turn wrote the transcript, the thread state, the candidate snapshot
      // and the run row server-side. None of it arrived over the stream, so all
      // of it has to be re-read — and only now, because refetching mid-stream
      // would race the text still arriving.
      if (finished.threadId) writeTurn(finished.threadId)
    },
  })

  // Land on the newest thread. An empty bench with a list beside it would make
  // the first thing a user does be a pointless click.
  useEffect(() => {
    if (activeId || !threads.data?.length) return
    setActiveId(threads.data[0].thread_id)
  }, [activeId, threads.data])

  // The run being inspected follows the live turn until the user picks another
  // one, so the replay shows this turn's tool chain the moment it exists. With
  // no turn in flight it falls back to the thread's newest run: opening a thread
  // should not land on an empty panel when its last run is right there.
  const runList = runs.data ?? []
  const runId = selectedRunId ?? turn.state.runId ?? runList[0]?.run_id ?? null
  const runDetail = useRun(runId)

  const history = useMemo(
    () => withoutEchoedTurn(messages.data ?? [], prompt, turn.state),
    [messages.data, prompt, turn.state],
  )
  const citations = useMemo(() => citableIds(history, turn.state), [history, turn.state])
  const timeline = useMemo(
    () => mergeToolCalls(turn.state.tools, runDetail.data?.tool_calls),
    [turn.state.tools, runDetail.data],
  )
  const escaped = useMemo(
    () => unresolvedCitations(turn.state.text, turn.state.citations),
    [turn.state.text, turn.state.citations],
  )

  // Confirming is a write, so the response is kept and shown rather than folded
  // into a toast: the thread the user is looking at just changed state, and the
  // server's own sentence about what happened is the one to read.
  const decide = useMutation({
    mutationFn: (decision: 'confirm' | 'cancel') =>
      chatApi.confirm(activeId as string, decision),
    onSuccess: (result) => {
      setConfirmOutcome(result)
      if (activeId) writeTurn(activeId)
    },
  })

  // A memory event is the only signal that the panel behind the rail tab is now
  // stale. The token is how the panel hears about it without the page reaching
  // into its query.
  const memoryCount = turn.state.memories.length
  useEffect(() => {
    if (memoryCount > 0) setMemoryToken((n) => n + 1)
  }, [memoryCount])

  const resetTurn = useCallback(() => {
    turn.stop()
    turn.reset()
    setPrompt(null)
    setSelectedRunId(null)
    setCitationId(null)
    setConfirmOutcome(null)
  }, [turn])

  const switchThread = useCallback(
    (threadId: string) => {
      if (threadId === activeId) return
      // A turn belongs to its thread: carrying its text or its tool list into
      // another conversation would attribute one thread's run to another.
      resetTurn()
      setActiveId(threadId)
    },
    [activeId, resetTurn],
  )

  const handleCreate = useCallback(async () => {
    try {
      const created = await createThread.mutateAsync(undefined)
      resetTurn()
      setActiveId(created.thread_id)
    } catch {
      // The list's own error surface reports it; a toast on top of it would just
      // say the same thing twice.
    }
  }, [createThread, resetTurn])

  const commitIdentity = useCallback(
    (next: string) => {
      const applied = next.trim() || 'demo-user'
      setIdentity(applied)
      if (applied === getUserId()) return
      setUserId(applied)
      // Identity is the entire isolation mechanism until M6 lands a real
      // principal, so changing it has to drop every thread-scoped read rather
      // than show one user the last user's conversation.
      resetTurn()
      setActiveId(null)
      void client.invalidateQueries()
    },
    [client, resetTurn],
  )

  const handleSend = useCallback(
    (content: string) => {
      if (!activeId) return
      setPrompt(content)
      setSelectedRunId(null)
      // A new turn supersedes the last gate: leaving the previous outcome on
      // screen next to a fresh question would read as an answer to it.
      setConfirmOutcome(null)
      turn.send(activeId, content)
    },
    [activeId, turn],
  )

  return (
    <>
      <PageHeader
        title="Agent Console"
        description="对话验证台：一轮问答的过程、引用与工具链都在这一屏。"
        extra={
          <label className="flex items-center gap-2 text-[12px] text-ink-tertiary">
            当前身份 X-User-ID
            <Input
              value={identity}
              aria-label="X-User-ID"
              onChange={(event) => setIdentity(event.target.value)}
              onBlur={(event) => commitIdentity(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter') commitIdentity(event.currentTarget.value)
              }}
              className="h-8 w-[160px]"
            />
          </label>
        }
      />

      <div className="flex h-[calc(100vh-190px)] min-h-[560px] gap-4">
        <aside className="w-[240px] shrink-0">
          <ThreadList
            threads={threads.data ?? []}
            activeId={activeId}
            onSelect={switchThread}
            onCreate={() => void handleCreate()}
            creating={createThread.isPending}
            loading={threads.isLoading}
            error={threads.error}
            onRetry={() => void threads.refetch()}
          />
        </aside>

        <section className="flex min-w-0 flex-1 flex-col rounded-xl border border-[var(--border-subtle)] bg-surface px-3 backdrop-blur-xl">
          {!activeId ? (
            <EmptyState
              title="先新建或选一个会话"
              description={`当前身份是 ${identity}。点左侧「新建」，再说一句像「布鲁克林 4 星以上的拉面，要安静，推荐 3 家并说明依据」的话。`}
            />
          ) : thread.error && !messages.data ? (
            <ErrorState
              title="会话读取失败"
              description={
                thread.error instanceof Error ? thread.error.message : String(thread.error)
              }
              onRetry={() => void thread.refetch()}
            />
          ) : (
            <>
              <MessageList
                messages={history}
                prompt={prompt}
                turn={turn.state}
                onCitationClick={setCitationId}
                onShowMemories={() => setTab(tabUtilities)}
              />
              <GateBanner
                awaiting={turn.state.awaiting}
                confirmation={turn.state.confirmation}
                onDecide={(decision) => decide.mutate(decision)}
                deciding={decide.isPending}
                outcome={confirmOutcome}
                error={decide.error instanceof Error ? decide.error.message : null}
              />
              <TurnStatusBar turn={turn.state} modelName={runDetail.data?.model_name} />
              <Composer
                onSend={handleSend}
                onStop={turn.stop}
                running={turn.running}
                disabled={!activeId}
              />
            </>
          )}
        </section>

        <aside className="w-[380px] shrink-0 overflow-auto rounded-xl border border-[var(--border-subtle)] bg-surface px-3 py-3 backdrop-blur-xl">
          <Tabs value={tab} onValueChange={setTab}>
            <TabsList>
              <TabsTrigger value={tabTools}>
                <Wrench className="mr-1.5 h-3.5 w-3.5" />
                工具链
              </TabsTrigger>
              <TabsTrigger value={tabCitations}>
                <Quote className="mr-1.5 h-3.5 w-3.5" />
                引用
                {citations.length > 0 && <span className="ml-1 tabular">{citations.length}</span>}
              </TabsTrigger>
              <TabsTrigger value={tabUtilities}>
                <SlidersHorizontal className="mr-1.5 h-3.5 w-3.5" />
                记忆 / 探针
              </TabsTrigger>
            </TabsList>

            <TabsContent value={tabTools} className="space-y-4">
              <section className="space-y-2">
                <p className="m-0 text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">
                  本轮实时
                </p>
                <ToolTimeline calls={timeline} />
              </section>

              <CandidateCards candidates={candidates.data ?? []} />

              <section className="space-y-2 border-t border-[var(--border-subtle)] pt-3">
                <RunReplayPanel
                  runs={runList}
                  selectedRunId={runId}
                  onSelect={setSelectedRunId}
                  detail={runDetail.data}
                  loading={runs.isLoading}
                  detailLoading={runDetail.isLoading}
                  error={runs.error}
                  detailError={runDetail.error}
                  onRetry={() => void runs.refetch()}
                />
              </section>
            </TabsContent>

            <TabsContent value={tabCitations}>
              <CitationList ids={citations} escaped={escaped} onOpen={setCitationId} />
            </TabsContent>

            <TabsContent value={tabUtilities} className="space-y-4">
              <MemoryPanel refreshToken={memoryToken} />
              <div className="border-t border-[var(--border-subtle)] pt-3">
                {/* The probe needs a thread to hand its sentence to, so the
                    "send to the agent" button only appears when there is one. */}
                <SlotProbeCard onSendToAgent={activeId ? handleSend : undefined} />
              </div>
            </TabsContent>
          </Tabs>
        </aside>
      </div>

      <CitationDrawer evidenceId={citationId} onClose={() => setCitationId(null)} />
    </>
  )
}

// CitationList is the set of footnotes the transcript can honour.
//
// It is the index of the drawer: the same documents a [^n] opens, listed so the
// whole citation set of a turn can be read at once instead of one marker at a
// time.
function CitationList({
  ids,
  escaped,
  onOpen,
}: {
  ids: number[]
  escaped: number[]
  onOpen: (evidenceId: number) => void
}) {
  return (
    <div className="space-y-2">
      {escaped.length > 0 && (
        // A footnote with nothing behind it is a hard failure, not a cosmetic
        // one: it is an assertion the answer cannot support. It gets said out
        // loud rather than rendered as a dead marker.
        <div className="rounded-lg border border-error/20 bg-error/5 px-2.5 py-2 text-[11px] text-error">
          引用越界：正文里的 {escaped.map((id) => `[^${id}]`).join('、')} 不在本轮证据集里，无法核对。
        </div>
      )}
      {ids.length === 0 ? (
        <EmptyState
          title="还没有引用"
          description="回答里的 [^n] 会落在这里，点开可以看到证据原文与数据时间。"
        />
      ) : (
        <ul className="m-0 list-none space-y-1 p-0">
          {ids.map((id) => (
            <li key={id}>
              <button
                type="button"
                onClick={() => onOpen(id)}
                className="flex w-full items-center justify-between gap-2 rounded-lg border border-[var(--border-subtle)] bg-surface-solid px-2.5 py-2 text-left text-[12px] transition-colors hover:bg-black/[0.03]"
              >
                <span className="font-mono text-ink">[^{id}]</span>
                <span className="text-[11px] text-ink-tertiary">查看原文</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
