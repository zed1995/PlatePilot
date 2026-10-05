// step-timeline.tsx is the inline projection of the runner's phase + step
// events onto the transcript.
//
// The right-rail ToolTimeline shows tool calls; this one shows the work
// the agent was doing when no tool was on the wire. Together they answer
// the question a user has during a two-minute turn: "what is it doing
// right now". The list grows as phase.started arrives and ticks the next
// phase when phase.finished closes the running one; the running row sits
// above the assistant Bubble and disappears when message.end turns the
// turn into a settled record.
//
// The component is presentation: a phase row plus an optional nested
// step list, and three icons for running / ok / failed. Anything that
// affects the model output lives in the reducer, not here.
import { Check, Loader2, X } from 'lucide-react'

import type { PhaseView } from '../../hooks/useAgentTurn'

export function StepTimeline({ phases }: { phases: PhaseView[] }) {
  if (phases.length === 0) return null
  return (
    <ul
      className="m-0 flex flex-col gap-1.5 p-0"
      data-testid="step-timeline"
      aria-label="本轮执行步骤"
    >
      {phases.map((phase) => (
        <li
          key={phase.phase_id}
          className="rounded-lg border border-[var(--border-subtle)] bg-surface-solid px-3 py-2 text-[12px]"
          data-testid={`step-phase-${phase.phase}`}
        >
          <div className="flex items-center gap-2">
            <PhaseIcon status={phase.status} />
            <span className="flex-1 text-ink">{phase.title}</span>
            {phase.status === 'running' && (
              <span className="text-[10px] uppercase tracking-[0.04em] text-info">进行中</span>
            )}
          </div>
          {phase.steps.length > 0 && (
            <ol className="m-0 mt-1.5 flex flex-col gap-1 border-t border-[var(--border-subtle)] pl-5 pt-1.5">
              {phase.steps.map((step) => (
                <li key={step.step_id} className="flex items-center gap-2">
                  <StepIcon status={step.status} />
                  <span className="text-ink-secondary">{step.title}</span>
                  {step.status === 'running' && (
                    <span className="text-[10px] uppercase tracking-[0.04em] text-info">进行中</span>
                  )}
                </li>
              ))}
            </ol>
          )}
        </li>
      ))}
    </ul>
  )
}

function PhaseIcon({ status }: { status: PhaseView['status'] }) {
  if (status === 'running') return <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-info" />
  if (status === 'failed')
    return <X className="h-3.5 w-3.5 shrink-0 text-error" aria-label="失败" />
  return <Check className="h-3.5 w-3.5 shrink-0 text-success" aria-label="已完成" />
}

function StepIcon({ status }: { status: PhaseView['steps'][number]['status'] }) {
  if (status === 'running') return <Loader2 className="h-3 w-3 shrink-0 animate-spin text-info" />
  if (status === 'failed')
    return <X className="h-3 w-3 shrink-0 text-error" aria-label="失败" />
  return <Check className="h-3 w-3 shrink-0 text-success" aria-label="已完成" />
}