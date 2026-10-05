// thread-list.tsx is the sidebar of the console: every thread this identity has,
// newest first, with the one piece of state that matters on a row.
//
// A thread that is waiting on the user looks different from one that merely
// finished, because that is the only thing a list can say that the user cannot
// otherwise see. `awaiting_clarification` and `awaiting_confirmation` are both
// waiting states but they ask for different things — an answer versus a
// decision — so they get different tones rather than one "waiting" badge.
//
// Deleting lives here rather than on the conversation view because the row is
// the object being removed: the user points at a conversation in the list, and
// the confirmation has to keep naming that one after a stray click.
import { useState } from 'react'
import { Loader2, Plus, Trash2 } from 'lucide-react'

import type { Thread, ThreadState } from '../../api/types'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { ErrorState } from '../error-state'
import { StatusTag, type StatusTone } from '../status-tag'
import { cn } from '../../lib/utils'

const stateTone: Record<string, StatusTone> = {
  idle: 'grey',
  completed: 'green',
  awaiting_clarification: 'orange',
  awaiting_confirmation: 'blue',
  failed: 'red',
}

// stateLabel is the human line on the badge. The raw enum stays in the title
// attribute: this console is read by whoever is debugging the agent, and hiding
// the exact state behind a pleasant label would cost them the one string they
// grep for in the logs.
const stateLabel: Record<string, string> = {
  idle: '空闲',
  completed: '已完成',
  awaiting_clarification: '等你回答',
  awaiting_confirmation: '等你确认',
  failed: '失败',
}

export function threadStateTone(state: ThreadState | string): StatusTone {
  return stateTone[state] ?? 'grey'
}

export function threadStateLabel(state: ThreadState | string): string {
  return stateLabel[state] ?? String(state)
}

export interface ThreadListProps {
  threads: Thread[]
  activeId: string | null
  onSelect: (threadId: string) => void
  onCreate: () => void
  creating?: boolean
  loading?: boolean
  error?: unknown
  onRetry?: () => void
  // onDelete is absent in a deployment that cannot delete — the row then has no
  // trash affordance at all, rather than one that is present and inert.
  onDelete?: (threadId: string) => void
  deleting?: boolean
  deleteError?: unknown
}

export function ThreadList({
  threads,
  activeId,
  onSelect,
  onCreate,
  creating = false,
  loading = false,
  error,
  onRetry,
  onDelete,
  deleting = false,
  deleteError,
}: ThreadListProps) {
  // The row awaiting confirmation is held here rather than in the page: it is
  // which-row state, and only this component knows the row the user clicked.
  const [pendingDelete, setPendingDelete] = useState<Thread | null>(null)

  return (
    <div className="flex h-full flex-col rounded-xl border border-[var(--border-subtle)] bg-surface backdrop-blur-xl">
      <div className="flex items-center justify-between gap-2 border-b border-[var(--border-subtle)] px-3 py-2.5">
        <span className="text-[11px] font-medium uppercase tracking-[0.04em] text-ink-tertiary">
          会话
        </span>
        <Button variant="ghost" size="sm" onClick={onCreate} disabled={creating}>
          {creating ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Plus className="h-3.5 w-3.5" />}
          新建
        </Button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-2">
        {error ? (
          <ErrorState
            title="会话列表加载失败"
            description={error instanceof Error ? error.message : String(error)}
            onRetry={onRetry}
          />
        ) : loading && threads.length === 0 ? (
          <p className="px-2 py-6 text-center text-[12px] text-ink-tertiary">加载中…</p>
        ) : threads.length === 0 ? (
          <p className="px-3 py-6 text-center text-[12px] text-ink-tertiary">
            还没有会话。点「新建」开始一次对话。
          </p>
        ) : (
          <ul className="m-0 list-none space-y-1 p-0">
            {threads.map((thread) => {
              const active = thread.thread_id === activeId
              const label = thread.title || '未命名会话'
              return (
                <li key={thread.thread_id} className="group relative">
                  <button
                    type="button"
                    onClick={() => onSelect(thread.thread_id)}
                    className={cn(
                      'flex w-full flex-col gap-1 rounded-lg py-2 pl-2.5 text-left transition-colors',
                      // The row reserves the width of the trash button whether
                      // or not it can be seen, so revealing it on hover does not
                      // reflow the title under the pointer.
                      onDelete ? 'pr-9' : 'pr-2.5',
                      active ? 'bg-black/[0.06]' : 'hover:bg-black/[0.03]',
                    )}
                  >
                    <span className="truncate text-[13px] font-medium text-ink">{label}</span>
                    <span className="flex items-center gap-1.5">
                      <span title={String(thread.current_state)}>
                        <StatusTag tone={threadStateTone(thread.current_state)}>
                          {threadStateLabel(thread.current_state)}
                        </StatusTag>
                      </span>
                      <span className="truncate text-[11px] text-ink-tertiary tabular">
                        {shortId(thread.thread_id)}
                      </span>
                    </span>
                  </button>
                  {onDelete && (
                    // A sibling of the select button rather than a child of it:
                    // a button inside a button is invalid, and the browser picks
                    // one of the two clicks to honour.
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label={`删除会话 ${label}`}
                      title="删除会话"
                      onClick={() => setPendingDelete(thread)}
                      className="absolute right-1 top-1/2 h-7 w-7 -translate-y-1/2 text-ink-tertiary opacity-0 transition-opacity hover:text-error focus-visible:opacity-100 group-hover:opacity-100"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </div>

      {deleteError ? (
        <p className="m-0 border-t border-[var(--border-subtle)] px-3 py-2 text-[11px] text-error">
          删除失败：
          {deleteError instanceof Error ? deleteError.message : String(deleteError)}
        </p>
      ) : null}

      {/* Deleting is not undoable, so it goes through a dialog rather than a
          second click on the same icon. The dialog closes on confirm: the row
          it names is about to disappear, and keeping a modal open in front of
          the list it just changed hides the feedback the user is owed. */}
      <Dialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>删除这个会话？</DialogTitle>
            <DialogDescription>
              「{pendingDelete?.title || '未命名会话'}」的对话记录、候选结果、工具链与审计记录会一并删除，删除后不可恢复。
            </DialogDescription>
          </DialogHeader>
          <div className="flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={() => setPendingDelete(null)}>
              取消
            </Button>
            <Button
              variant="destructive"
              size="sm"
              disabled={deleting}
              onClick={() => {
                if (!pendingDelete) return
                onDelete?.(pendingDelete.thread_id)
                setPendingDelete(null)
              }}
            >
              {deleting && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
              删除
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}

// shortId keeps the row scannable while remaining copy-pasteable into curl.
function shortId(threadId: string): string {
  return threadId.length > 8 ? threadId.slice(0, 8) : threadId
}
