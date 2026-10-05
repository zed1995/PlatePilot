import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'

import { StepTimeline } from './step-timeline'
import type { PhaseView } from '../../hooks/useAgentTurn'

function phase(
  phase_id: string,
  status: PhaseView['status'],
  title: string,
  steps: PhaseView['steps'] = [],
): PhaseView {
  return { phase: 'ingress', phase_id, title, status, steps }
}

describe('StepTimeline', () => {
  it('renders nothing when no phases have arrived', () => {
    const { container } = render(<StepTimeline phases={[]} />)
    expect(container.firstChild).toBeNull()
  })

  it('shows the running phase with a running icon', () => {
    render(
      <StepTimeline
        phases={[
          phase('ingress-1', 'running', '正在加载会话上下文'),
          phase('plan-1', 'ok', '正在制定下一步计划'),
        ]}
      />,
    )
    const list = screen.getByTestId('step-timeline')
    expect(list).toBeInTheDocument()
    expect(list.children).toHaveLength(2)
    expect(screen.getByText('正在加载会话上下文')).toBeInTheDocument()
    expect(screen.getByText('正在制定下一步计划')).toBeInTheDocument()
    expect(screen.getAllByText('进行中')).toHaveLength(1)
  })

  it('renders a failed phase as red', () => {
    render(<StepTimeline phases={[phase('answer-1', 'failed', '正在生成回答')]} />)
    expect(screen.getByLabelText('失败')).toBeInTheDocument()
    expect(screen.queryByText('进行中')).not.toBeInTheDocument()
  })

  it('nests step rows under their phase', () => {
    const steps: PhaseView['steps'] = [
      { step: 'loading_context', step_id: 'ingress-1-loading_context', title: '读取最近会话与候选快照', status: 'ok' },
      { step: 'embedding_memory', step_id: 'ingress-1-embedding_memory', title: '检索长期记忆', status: 'running' },
    ]
    render(<StepTimeline phases={[phase('ingress-1', 'running', '正在加载会话上下文', steps)]} />)
    expect(screen.getByText('读取最近会话与候选快照')).toBeInTheDocument()
    expect(screen.getByText('检索长期记忆')).toBeInTheDocument()
  })
})