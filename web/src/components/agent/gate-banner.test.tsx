import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import type { Gate } from '../../hooks/useAgentTurn'
import { GateBanner, clarifySentence, describeSlots } from './gate-banner'

// The regression this guards: the agent answered a follow-up by printing
// `restaurant_id` at the user, because the only string the banner had was the
// slot's field name. A slot name belongs on a struct, never on screen.
test('a clarification never prints the internal slot name', () => {
  const gate: Gate = {
    kind: 'clarification',
    state: 'awaiting_clarification',
    pendingAction: 'resolve_restaurant',
    missingSlots: ['restaurant_id'],
  }
  render(<GateBanner awaiting={gate} onDecide={vi.fn()} />)

  expect(screen.queryByText(/restaurant_id/)).not.toBeInTheDocument()
  expect(screen.getByText(/具体是哪一家店/)).toBeInTheDocument()
  // The state itself is fine — it is the string that matches the server logs.
  expect(screen.getByText(/awaiting_clarification/)).toBeInTheDocument()
})

test('describeSlots translates, dedupes, and never echoes an unknown name', () => {
  expect(describeSlots(['cuisine', 'cuisines'])).toBe('想吃什么菜系')
  expect(describeSlots(['date', 'party_size'])).toBe('哪一天、几个人')
  expect(describeSlots(['something_new'])).toBe('一项补充信息')
  expect(describeSlots([])).toBe('')
})

test('clarifySentence has a line for a gate with no slots named', () => {
  expect(clarifySentence({ kind: 'clarification', state: 'awaiting_clarification' })).toBe(
    '系统在等你的下一条消息。',
  )
})

test('a confirmation offers the write and the escape, and reports which was chosen', async () => {
  const user = userEvent.setup()
  const onDecide = vi.fn()
  const gate: Gate = {
    kind: 'confirmation',
    state: 'awaiting_confirmation',
    pendingAction: 'request_reservation',
    summary: '准备为 Tabetomo 预约 2 人，明晚 19:00。',
  }
  render(<GateBanner confirmation={gate} onDecide={onDecide} />)

  expect(screen.getByText('准备为 Tabetomo 预约 2 人，明晚 19:00。')).toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: /确认/ }))
  expect(onDecide).toHaveBeenCalledWith('confirm')

  await user.click(screen.getByRole('button', { name: /取消/ }))
  expect(onDecide).toHaveBeenLastCalledWith('cancel')
  expect(onDecide).toHaveBeenCalledTimes(2)
})

// Confirming twice would be a second reservation. The idempotent replay is
// reported as such rather than as a fresh success.
test('a replayed confirmation says it was already handled', () => {
  const gate: Gate = { kind: 'confirmation', state: 'awaiting_confirmation', summary: '取消预约。' }
  render(
    <GateBanner
      confirmation={gate}
      onDecide={vi.fn()}
      outcome={{
        thread_id: 't1',
        decision: 'confirm',
        state: 'completed',
        replayed: true,
        message: '已处理过。',
      }}
    />,
  )

  expect(screen.getByText(/已经处理过/)).toBeInTheDocument()
  expect(screen.getByText(/state=completed/)).toBeInTheDocument()
})

test('a confirmation with no pending gate renders nothing at all', () => {
  const { container } = render(<GateBanner onDecide={vi.fn()} />)
  expect(container).toBeEmptyDOMElement()
})
