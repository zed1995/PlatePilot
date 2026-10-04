// run-replay-panel.tsx is the after-the-fact half of the tool timeline.
//
// The live stream reports that a tool ran and how long it took. The replay
// reports what it returned and what it was called with. Neither is complete on
// its own and this page would be lying if it presented the live view as the
// whole story — so the replay is a first-class panel rather than a debugging
// extra.
//
// `arguments` is redacted on the server and is shown verbatim, folded. Prettying
// it up would mean writing a second, divergent description of what the agent
// sent, which is the one thing an operator must be able to trust.
import { EmptyState } from '../empty-state'
import { ErrorState } from '../error-state'
import { JsonBlock } from '../json-block'
import { StatusTag, type StatusTone } from '../status-tag'
import { formatDuration, formatTime } from '../../format'
import { cn } from '../../lib/utils'
import type { RunDetail, RunView, ToolCallView } from '../../api/types'

export interface RunReplayPanelProps {
  runs: RunView[]
  selectedRunId: string | null
  onSelect: (runId: string) => void
  detail?: RunDetail
  loading?: boolean
  detailLoading?: boolean
  error?: unknown
  detailError?: unknown
  onRetry?: () => void
}

function runTone(status: string): StatusTone {
  switch (status) {
    case 'ok':
    case 'success':
    case 'succeeded':
    case 'completed':
      return 'green'
    case 'running':
    case 'started':
      return 'blue'
    case 'failed':
    case 'error':
      return 'red'
    default:
      return 'grey'
  }
}

export function RunReplayPanel({
  runs,
  selectedRunId,
  onSelect,
  detail,
  loading = false,
  detailLoading = false,
  error,
  detailError,
  onRetry,
}: RunReplayPanelProps) {
  if (error) {
    return (
      <ErrorState
        title="run 列表加载失败"
        description={error instanceof Error ? error.message : String(error)}
        onRetry={onRetry}
      />
    )
  }
  if (loading && runs.length === 0) {
    return <p className="m-0 text-[12px] text-ink-tertiary">读取 run…</p>
  }
  if (runs.length === 0) {
    return (
      <EmptyState
        title="还没有 run"
        description="一轮对话结束后，这里按时间倒序列出运行记录，点开可以看它的完整工具链。"
      />
    )
  }

  return (
    <div className="space-y-2" data-testid="run-replay-panel">
      <p className="m-0 text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">历史 run</p>
      <ul className="m-0 list-none space-y-1 p-0">
        {runs.map((run) => (
          <li key={run.run_id}>
            <button
              type="button"
              onClick={() => onSelect(run.run_id)}
              aria-pressed={run.run_id === selectedRunId}
              className={cn(
                'flex w-full flex-col gap-1 rounded-lg px-2.5 py-2 text-left transition-colors',
                run.run_id === selectedRunId ? 'bg-black/[0.06]' : 'hover:bg-black/[0.03]',
              )}
            >
              <span className="flex items-center gap-2">
                <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-ink">
                  {run.run_id}
                </span>
                <StatusTag tone={runTone(run.status)}>{run.status}</StatusTag>
              </span>
              <span className="flex flex-wrap items-center gap-x-2.5 text-[11px] text-ink-tertiary tabular">
                {run.model_name && <span>{run.model_name}</span>}
                <span>{formatTime(run.started_at)}</span>
                {run.latency_ms !== undefined && <span>{formatDuration(run.latency_ms)}</span>}
                {run.tool_call_count !== undefined && <span>工具 {run.tool_call_count} 次</span>}
              </span>
            </button>
          </li>
        ))}
      </ul>

      {selectedRunId && (
        <div className="space-y-2 border-t border-[var(--border-subtle)] pt-2">
          <p className="m-0 text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">
            {selectedRunId} 的工具链
          </p>
          {detailLoading && !detail ? (
            <p className="m-0 text-[12px] text-ink-tertiary">读取工具链…</p>
          ) : detailError ? (
            <ErrorState
              title="run 详情加载失败"
              description={
                detailError instanceof Error ? detailError.message : String(detailError)
              }
            />
          ) : detail ? (
            detail.tool_calls.length === 0 ? (
              <EmptyState title="这一轮没有调用工具" description="它直接回答了。" />
            ) : (
              <ol className="m-0 list-none space-y-2 p-0">
                {detail.tool_calls.map((call, index) => (
                  <ToolCallCard key={call.call_id} call={call} index={index} />
                ))}
              </ol>
            )
          ) : null}
        </div>
      )}
    </div>
  )
}

function ToolCallCard({ call, index }: { call: ToolCallView; index: number }) {
  return (
    <li className="rounded-lg border border-[var(--border-subtle)] bg-surface-solid px-3 py-2">
      <div className="flex items-center gap-2">
        <span className="w-4 shrink-0 text-right text-[11px] text-ink-tertiary tabular">
          {index + 1}
        </span>
        <code className="min-w-0 flex-1 truncate font-mono text-[12px] text-ink">
          {call.tool_name}
        </code>
        {call.latency_ms !== undefined && (
          <span className="shrink-0 text-[11px] text-ink-tertiary tabular">
            {formatDuration(call.latency_ms)}
          </span>
        )}
        <StatusTag tone={runTone(call.status)}>{call.status}</StatusTag>
      </div>

      {call.result_summary && (
        <p className="m-0 mt-1.5 whitespace-pre-wrap border-t border-[var(--border-subtle)] pt-1.5 text-[11px] text-ink-secondary">
          {call.result_summary}
        </p>
      )}

      {call.arguments !== undefined && call.arguments !== null && (
        <details className="mt-1.5">
          <summary className="cursor-pointer text-[11px] text-ink-tertiary">
            arguments（已脱敏，原样展示）
          </summary>
          <div className="pt-1.5">
            <JsonBlock value={call.arguments} />
          </div>
        </details>
      )}
    </li>
  )
}
