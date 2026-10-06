// message-list.tsx renders the transcript plus the turn currently in flight.
//
// Two sources feed one scroll area: the persisted history and the stream. They
// are merged here rather than by the caller because only this component knows
// what "the same message" looks like — the page hands over both and this decides
// where the history ends and the live turn begins.
import { useEffect, useRef } from 'react'

import type { MessageView } from '../../api/types'
import type { MemorySavedItem, TurnState } from '../../hooks/useAgentTurn'
import { ErrorState } from '../error-state'
import { AnswerText } from './answer-text'
import { StepTimeline } from './step-timeline'
import { cn } from '../../lib/utils'

export interface MessageListProps {
  messages: MessageView[]
  // prompt is the text of the turn in flight, shown as the user's own bubble
  // before the server has it in the transcript.
  prompt?: string | null
  turn: TurnState
  onCitationClick?: (evidenceId: number) => void
  onShowMemories?: () => void
}

export function MessageList({
  messages,
  prompt,
  turn,
  onCitationClick,
  onShowMemories,
}: MessageListProps) {
  const scroller = useRef<HTMLDivElement | null>(null)
  const streaming = turn.phase === 'streaming'

  // Follow the tail while text arrives. Only while streaming: yanking the
  // viewport on a re-read of history would fight the user's scroll.
  //
  // The deps cover both the bubble text and the step timeline: a phase
  // change adds height to the transcript and the user would have to scroll
  // manually to keep watching it grow otherwise.
  useEffect(() => {
    if (!streaming) return
    const node = scroller.current
    if (node) node.scrollTop = node.scrollHeight
  }, [streaming, turn.text, turn.thinking.length, turn.tools.length, turn.phases.length])

  const hasTurn = Boolean(prompt) || Boolean(turn.text) || turn.tools.length > 0

  return (
    <div ref={scroller} className="min-h-0 flex-1 space-y-3 overflow-auto px-1 py-2">
      {messages.map((message) => (
        <Bubble
          key={message.message_id}
          role={message.role}
          content={message.content}
          citable={message.evidence_ids}
          onCitationClick={onCitationClick}
        />
      ))}

      {prompt && <Bubble role="user" content={prompt} />}

      {/* The inline step timeline appears while the turn is in flight and
          fades out of relevance once it ends: a settled turn's phases
          belong to history, and the right-rail replay already shows them.
          The component renders nothing when no phase events have arrived,
          so a deployment that turned AGENT_PHASE_EVENTS off gets exactly
          the same render as before this change. */}
      {streaming && <StepTimeline phases={turn.phases} />}

      {/* The model's reasoning is shown on its own channel, never merged into
          the answer. On a reasoning model it is the only output for the long
          stretch before the first answer token, and a silent spinner of a
          minute is exactly what it exists to replace. It is open while the
          model is still thinking and folds once the answer starts, so a
          finished turn reads as the answer rather than the scratch work. */}
      {turn.thinking && <ThinkingTrace text={turn.thinking} live={streaming && !turn.text} />}

      {hasTurn && (
        <Bubble
          role="assistant"
          content={turn.text}
          citable={turn.citations}
          onCitationClick={onCitationClick}
          streaming={streaming}
        />
      )}

      {turn.phase === 'failed' && (
        <ErrorState title="本轮回答失败" description={turn.error ?? '未知错误'} />
      )}

      {/* A memory is written as a side effect and reported once. If it is not
          shown here the user has no way to know the agent just recorded
          something about them, and no reason to ever open the panel that could
          correct it. */}
      {turn.memories.map((memory) => (
        <MemoryNotice key={memory.memoryId} memory={memory} onOpen={onShowMemories} />
      ))}

      {turn.phase === 'interrupted' && (
        <p className="m-0 rounded-lg border border-warning/20 bg-warning/5 px-3 py-2 text-[12px] text-warning">
          本轮未正常结束：流已关闭但没有收到结束事件。文本可能不完整，可以重发一次。
        </p>
      )}

      {turn.phase === 'canceled' && (
        <p className="m-0 px-1 text-[12px] text-ink-tertiary">已停止本轮回答。</p>
      )}
    </div>
  )
}

// ThinkingTrace renders the model's reasoning. It is deliberately a
// <details> so a long chain of thought cannot push the answer off screen: it
// is open while the model is still thinking and folds once the answer starts.
// The key forces a remount when that flips, because `open` is only a default
// the browser honours on mount.
function ThinkingTrace({ text, live }: { text: string; live: boolean }) {
  return (
    <details
      key={live ? 'live' : 'settled'}
      open={live}
      data-testid="thinking-trace"
      className="rounded-xl border border-[var(--border-subtle)] bg-black/[0.02] px-3 py-2 text-[12px] text-ink-tertiary"
    >
      <summary className="cursor-pointer select-none text-ink-secondary">
        {live ? '思考中…' : '思考过程'}
      </summary>
      <div className="mt-1.5 whitespace-pre-wrap leading-[1.6]">{text}</div>
    </details>
  )
}

function MemoryNotice({
  memory,
  onOpen,
}: {
  memory: MemorySavedItem
  onOpen?: () => void
}) {
  return (
    <div
      data-testid={`memory-notice-${memory.memoryId}`}
      className="flex items-center gap-2 rounded-xl border border-[var(--border-subtle)] bg-black/[0.03] px-3 py-2 text-[12px] text-ink-secondary"
    >
      <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-info" />
      {/* "已更新" and "已记住" are different claims. The server tells us which
          one happened; using the wrong verb would make the panel look like it
          lost a row. */}
      <span>
        {memory.refreshed ? '已更新记忆' : '已记住'}：{memory.content}
      </span>
      <span className="font-mono text-[11px] text-ink-tertiary">{memory.memoryType}</span>
      {onOpen && (
        <button
          type="button"
          onClick={onOpen}
          className="ml-auto shrink-0 text-[11px] text-ink-secondary underline hover:text-ink"
        >
          查看 / 修改
        </button>
      )}
    </div>
  )
}

function Bubble({
  role,
  content,
  citable,
  onCitationClick,
  streaming = false,
}: {
  role: MessageView['role'] | string
  content: string
  citable?: number[]
  onCitationClick?: (evidenceId: number) => void
  streaming?: boolean
}) {
  const isUser = role === 'user'
  if (!content && !streaming) return null

  return (
    <div className={cn('flex', isUser ? 'justify-end' : 'justify-start')}>
      <div
        data-role={role}
        className={cn(
          'max-w-[92%] rounded-2xl px-3.5 py-2.5',
          isUser
            ? 'whitespace-pre-wrap bg-ink text-[13px] leading-[1.6] text-white'
            : 'border border-[var(--border-subtle)] bg-surface-solid',
        )}
      >
        {isUser ? (
          content
        ) : (
          <>
            <AnswerText content={content} citable={citable} onCitationClick={onCitationClick} />
            {streaming && (
              <span
                aria-hidden
                className="ml-0.5 inline-block h-3.5 w-[7px] animate-pulse bg-ink/40 align-middle"
              />
            )}
          </>
        )}
      </div>
    </div>
  )
}
