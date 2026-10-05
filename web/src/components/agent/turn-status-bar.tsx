// turn-status-bar.tsx is the one line under the transcript that says what the
// turn just did.
//
// It exists because a real turn takes two to two and a half minutes. Before the
// tool timeline is on screen, a bare spinner for two minutes is indistinguishable
// from a hang; a counter that is visibly moving is not. That is also why the
// elapsed time is computed locally while streaming rather than waiting for the
// server to report it at the end.
import { useEffect, useState } from 'react'
import { AlertTriangle } from 'lucide-react'

import type { TurnPhase, TurnState } from '../../hooks/useAgentTurn'
import { StatusTag, type StatusTone } from '../status-tag'
import { formatDuration } from '../../format'

const phaseTone: Record<TurnPhase, StatusTone> = {
  idle: 'grey',
  streaming: 'blue',
  done: 'green',
  failed: 'red',
  awaiting_user: 'orange',
  awaiting_confirm: 'orange',
  canceled: 'grey',
  interrupted: 'orange',
}

const phaseLabel: Record<TurnPhase, string> = {
  idle: '空闲',
  streaming: '运行中',
  done: '已完成',
  failed: '失败',
  awaiting_user: '等待你回答',
  awaiting_confirm: '等待你确认',
  canceled: '已停止',
  interrupted: '未正常结束',
}

export interface TurnStatusBarProps {
  turn: TurnState
  // modelName comes from the run replay; the live stream does not carry it.
  modelName?: string
}

export function TurnStatusBar({ turn, modelName }: TurnStatusBarProps) {
  const elapsed = useElapsed(turn)
  // The active phase is the row still showing the spinner. There is at
  // most one — phases run sequentially by design — and reading the title
  // here keeps the badge next to it accurate as the runner moves from plan
  // to tools to answer without the page having to know the phases exist.
  const activePhase = turn.phases.find((phase) => phase.status === 'running')

  if (turn.phase === 'idle') return null

  return (
    <div className="space-y-1.5 border-t border-[var(--border-subtle)] px-1 py-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-ink-tertiary">
        <StatusTag tone={phaseTone[turn.phase]}>{phaseLabel[turn.phase]}</StatusTag>
        {activePhase && (
          <span className="text-ink-secondary" data-testid="active-phase">
            {activePhase.title}
          </span>
        )}
        {modelName && <span>{modelName}</span>}
        {elapsed !== null && <span className="tabular">{formatDuration(elapsed)}</span>}
        {turn.tools.length > 0 && <span className="tabular">工具 {turn.tools.length} 次</span>}
        {turn.usage && (
          <span className="tabular">
            token {turn.usage.input_tokens}↑ {turn.usage.output_tokens}↓
          </span>
        )}
        {turn.finishReason && <span>finish_reason={turn.finishReason}</span>}
      </div>

      {turn.warnings.length > 0 && (
        // Warnings are not decoration. The vector channel degrading to a
        // document-order scan arrives here and nowhere else, and a degraded
        // answer that looks identical to a healthy one is a wrong answer.
        <div className="flex items-start gap-2 rounded-lg border border-warning/20 bg-warning/5 px-2.5 py-1.5 text-[11px] text-warning">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <ul className="m-0 list-disc space-y-0.5 pl-4">
            {turn.warnings.map((warning) => (
              <li key={warning}>{warning}</li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

// useElapsed ticks while a turn is running and freezes at the end.
//
// The tick is 500ms: fast enough that the counter never looks stuck, slow enough
// that it costs nothing next to a stream of deltas.
function useElapsed(turn: TurnState): number | null {
  const [, tick] = useState(0)
  const running = turn.phase === 'streaming'

  useEffect(() => {
    if (!running) return
    const timer = setInterval(() => tick((n) => n + 1), 500)
    return () => clearInterval(timer)
  }, [running])

  if (turn.elapsedMs !== undefined) return turn.elapsedMs
  if (!running || !turn.startedAt) return null
  return Date.now() - turn.startedAt
}
