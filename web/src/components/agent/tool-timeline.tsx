// tool-timeline.tsx is the reason a two-minute turn does not read as a hang.
//
// The live stream says a tool started and later that it finished; only the run
// replay says what it returned. So the same list is rendered twice in a turn's
// life — once while it runs, once when the replay lands — and the second render
// is strictly more informative. That is deliberate: they share one data
// structure and differ only in whether a summary is attached.
import { Loader2 } from 'lucide-react'

import type { ToolCallView } from '../../api/types'
import type { ToolCallItem } from '../../hooks/useAgentTurn'
import { formatDuration } from '../../format'
import { EmptyState } from '../empty-state'
import { StatusTag, type StatusTone } from '../status-tag'

export interface TimelineCall {
  call_id: string
  tool: string
  status: string
  latency_ms?: number
  // Only the replay carries this. Its absence is not an error, it is the live
  // stream being honest about what it knows.
  result_summary?: string
}

// mergeToolCalls joins the live timeline with the replayed one.
//
// Order comes from the live list, because that is the order the user watched.
// Pairing is by call_id and never by position: two tool calls overlap, and the
// replay is free to return them in whatever order the database likes.
export function mergeToolCalls(
  live: ToolCallItem[],
  replay: ToolCallView[] | undefined,
): TimelineCall[] {
  const byId = new Map((replay ?? []).map((call) => [call.call_id, call]))
  const merged = live.map((call) => {
    const replayed = byId.get(call.call_id)
    byId.delete(call.call_id)
    return {
      call_id: call.call_id,
      tool: call.tool,
      status: replayed?.status || call.status,
      latency_ms: call.latency_ms ?? replayed?.latency_ms,
      result_summary: replayed?.result_summary,
    }
  })
  // A call the replay knows about but the stream never announced would be lost
  // by taking the live list as complete, and a missing row in a tool chain is
  // exactly the kind of thing this page exists to make impossible to miss.
  for (const orphan of byId.values()) {
    merged.push({
      call_id: orphan.call_id,
      tool: orphan.tool_name,
      status: orphan.status,
      latency_ms: orphan.latency_ms,
      result_summary: orphan.result_summary,
    })
  }
  return merged
}

function toolTone(status: string): StatusTone {
  if (status === 'running') return 'blue'
  return status === 'ok' || status === 'success' || status === 'succeeded' || status === 'completed'
    ? 'green'
    : 'red'
}

export function ToolTimeline({ calls }: { calls: TimelineCall[] }) {
  if (calls.length === 0) {
    return (
      <EmptyState
        title="还没有工具调用"
        description="发一轮对话后，这里会实时出现每次工具调用的名字、状态与耗时。"
      />
    )
  }

  return (
    <ol className="m-0 list-none space-y-2 p-0" data-testid="tool-timeline">
      {calls.map((call, index) => (
        <li
          key={call.call_id}
          className="rounded-lg border border-[var(--border-subtle)] bg-surface-solid px-3 py-2"
        >
          <div className="flex items-center gap-2">
            <span className="w-4 shrink-0 text-right text-[11px] text-ink-tertiary tabular">
              {index + 1}
            </span>
            {call.status === 'running' ? (
              <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-info" />
            ) : (
              <span
                className={
                  toolTone(call.status) === 'green'
                    ? 'h-1.5 w-1.5 shrink-0 rounded-full bg-success'
                    : 'h-1.5 w-1.5 shrink-0 rounded-full bg-error'
                }
              />
            )}
            {/* The tool name is shown verbatim. Whoever debugs this agent greps
                for `search_restaurants`; "搜索餐厅" would be a translation of the
                only string they can act on. */}
            <code className="min-w-0 flex-1 truncate font-mono text-[12px] text-ink">
              {call.tool}
            </code>
            {call.latency_ms !== undefined && (
              <span className="shrink-0 text-[11px] text-ink-tertiary tabular">
                {formatDuration(call.latency_ms)}
              </span>
            )}
            <StatusTag tone={toolTone(call.status)}>{call.status}</StatusTag>
          </div>
          {call.result_summary && (
            <p className="m-0 mt-1.5 whitespace-pre-wrap border-t border-[var(--border-subtle)] pt-1.5 text-[11px] text-ink-secondary">
              {call.result_summary}
            </p>
          )}
        </li>
      ))}
    </ol>
  )
}
