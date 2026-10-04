import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import type { InterpretResult } from '../../api/types'
import { SlotProbeCard } from './slot-probe-card'

const interpret = vi.hoisted(() => vi.fn())

vi.mock('../../api/chat', () => ({
  chatApi: { interpret },
  getUserId: () => 'demo-user',
  setUserId: vi.fn(),
}))

const parsed: InterpretResult = {
  intent: 'discover',
  query: '意大利菜 安静',
  hard_filters: {
    borough: 'Manhattan',
    neighborhood: 'Midtown',
    cuisines: ['Italian'],
    min_rating: 4,
  },
  soft_conditions: [
    { text: '安静，适合约会', topic: 'ambience' },
    { text: '适合约会', topic: 'occasion' },
  ],
  named_restaurants: [],
  missing_slots: [],
  need_clarification: false,
  source: 'model',
  extract_latency_ms: 412,
}

beforeEach(() => {
  interpret.mockReset()
  interpret.mockResolvedValue(parsed)
})

async function probe(text = '曼哈顿中城 4 星以上意大利菜，要安静适合约会') {
  const user = userEvent.setup()
  render(<SlotProbeCard />)
  await user.type(screen.getByLabelText('探针输入'), text)
  await user.click(screen.getByRole('button', { name: /解析这句/ }))
  return user
}

// Hard filters are enforced by the structured channel and soft conditions are
// scored by the reranker. One list for both is how "安静" starts being treated
// as a filter, so the sections are separate elements and separate treatments.
test('hard and soft conditions land in separate sections', async () => {
  await probe()
  const result = await screen.findByTestId('probe-result')

  const hard = result.querySelector('section:nth-of-type(1)')
  const soft = result.querySelector('section:nth-of-type(2)')

  expect(hard).toHaveTextContent('hard_filters')
  expect(hard).toHaveTextContent('Manhattan')
  expect(hard).toHaveTextContent('Italian')
  expect(hard).not.toHaveTextContent('安静')

  expect(soft).toHaveTextContent('soft_conditions')
  expect(soft).toHaveTextContent('安静，适合约会')
  expect(soft).not.toHaveTextContent('min_rating')
})

test('the intent, the source and the extraction cost are all visible', async () => {
  await probe()

  expect(await screen.findByText('discover')).toBeInTheDocument()
  // `source=rules` is the difference between "the model read this" and "the
  // fallback read this"; it has to be on screen, not inferred.
  expect(screen.getByText('source=model')).toBeInTheDocument()
  expect(screen.getByText('412ms')).toBeInTheDocument()
})

test('the raw filter object is always reachable', async () => {
  await probe()
  await screen.findByTestId('probe-result')

  expect(screen.getByText(/hard_filters 原始 JSON/)).toBeInTheDocument()
  expect(screen.getByText(/"min_rating": 4/)).toBeInTheDocument()
})

test('a rules-only parse is labelled as such', async () => {
  interpret.mockResolvedValue({ ...parsed, source: 'rules', soft_conditions: [] })
  await probe()

  expect(await screen.findByText('source=rules')).toBeInTheDocument()
  expect(screen.getByText('没有解析出软条件。')).toBeInTheDocument()
})

test('the sentence can be handed to the agent for comparison', async () => {
  const onSendToAgent = vi.fn()
  const user = userEvent.setup()
  render(<SlotProbeCard onSendToAgent={onSendToAgent} />)

  await user.type(screen.getByLabelText('探针输入'), '第二家安静吗')
  await user.click(screen.getByRole('button', { name: /发给 Agent/ }))

  expect(onSendToAgent).toHaveBeenCalledWith('第二家安静吗')
})

test('a failed parse is reported instead of silently doing nothing', async () => {
  interpret.mockRejectedValue(new Error('upstream 502'))
  await probe()

  await waitFor(() => expect(screen.getByText(/upstream 502/)).toBeInTheDocument())
})
