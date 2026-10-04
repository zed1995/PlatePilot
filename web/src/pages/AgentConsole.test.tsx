import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

import type { StreamEvent } from '../api/chat'
import { chatApi } from '../api/chat'
import App from '../App'
import AgentConsole, { withoutEchoedTurn } from './AgentConsole'

// The stream is scripted rather than stubbed: a turn is a sequence, and the
// assertions that matter are about what the page shows between two frames, not
// about what it shows after the last one.
const harness = vi.hoisted(() => ({
  streams: [] as {
    emit: (event: unknown) => void
    end: () => void
  }[],
  sent: [] as { threadId: string; content: string }[],
}))

const thread = {
  thread_id: 'thread-1',
  title: '验收',
  current_state: 'idle',
  created_at: '2026-10-04T00:00:00Z',
  updated_at: '2026-10-04T00:00:00Z',
}

const history = {
  messages: [
    {
      message_id: 'm1',
      role: 'user',
      content: '布鲁克林 4 星以上的拉面',
      seq: 1,
      created_at: '2026-10-04T00:00:01Z',
    },
    {
      message_id: 'm2',
      role: 'assistant',
      content: '结论：第一家是 Tabetomo[^1]',
      evidence_ids: [1],
      seq: 2,
      created_at: '2026-10-04T00:00:02Z',
    },
  ],
}

const runView = {
  run_id: 'run-1',
  thread_id: 'thread-1',
  status: 'ok',
  model_name: 'qwen3-max',
  started_at: '2026-10-04T00:00:00Z',
  latency_ms: 91_000,
  tool_call_count: 1,
}

const runDetail = {
  ...runView,
  tool_calls: [
    {
      call_id: 'call-a',
      tool_name: 'search_restaurants',
      status: 'ok',
      latency_ms: 842,
      result_summary: '找到 8 家',
      created_at: '2026-10-04T00:00:01Z',
    },
  ],
}

const evidenceDoc = {
  evidence_id: 1,
  restaurant_id: 42,
  restaurant_name: 'Tabetomo',
  doc_type: 'restaurant_review_summary',
  content: '店内安静，适合聊天。',
  source: 'google_places',
  snapshot_at: '2026-09-30T12:00:00Z',
}

vi.mock('../api/chat', () => ({
  getUserId: () => 'demo-user',
  setUserId: vi.fn(),
  chatApi: {
    createThread: vi.fn(async () => thread),
    listThreads: vi.fn(async () => ({ conversations: [thread] })),
    getThread: vi.fn(async () => thread),
    listMessages: vi.fn(async () => history),
    listCandidates: vi.fn(async () => ({
      candidates: [{ position: 1, restaurant_id: 42, name: 'Tabetomo', score: 0.91 }],
    })),
    confirm: vi.fn(async (threadId: string, decision: string) => ({
      thread_id: threadId,
      decision,
      state: 'completed',
      message: '已按你的选择处理。',
    })),
    listMemories: vi.fn(async () => ({
      memories: [
        {
          id: 'mem-1',
          memory_type: 'dietary',
          content: '不吃辣',
          source: 'conversation',
          confidence: 0.9,
          created_at: '2026-10-01T00:00:00Z',
          updated_at: '2026-10-01T00:00:00Z',
        },
      ],
    })),
    updateMemory: vi.fn(),
    deleteMemory: vi.fn(),
    listRuns: vi.fn(async () => ({ runs: [runView] })),
    getRun: vi.fn(async () => runDetail),
    searchRestaurants: vi.fn(),
    fetchEvidence: vi.fn(async () => ({ evidence: [evidenceDoc] })),
    interpret: vi.fn(),
  },
  sendMessage: vi.fn(
    (opts: { threadId: string; content: string; onEvent: (event: StreamEvent) => void }) => {
      harness.sent.push({ threadId: opts.threadId, content: opts.content })
      let finish: () => void = () => {}
      const done = new Promise<void>((resolve) => {
        finish = resolve
      })
      harness.streams.push({ emit: opts.onEvent as (event: unknown) => void, end: finish })
      return { abort: finish, done }
    },
  ),
}))

function renderConsole() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <AgentConsole />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function stream(index = 0) {
  const found = harness.streams[index]
  if (!found) throw new Error(`stream ${index} was never opened`)
  return found
}

beforeEach(() => {
  harness.streams.length = 0
  harness.sent.length = 0
})

test('history renders in seq order with footnotes the server called citable', async () => {
  renderConsole()

  const first = await screen.findByText('布鲁克林 4 星以上的拉面')
  const second = await screen.findByText(/结论：第一家是/)
  expect(first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

  // A footnote the citation event vouched for is clickable; one that was not
  // would render as a dead marker instead of opening an empty drawer.
  const marker = screen.getByRole('button', { name: '1' })
  expect(marker).toBeEnabled()
})

test('sending disables the input, streams text, and re-enables at the end', async () => {
  const user = userEvent.setup()
  renderConsole()

  const input = await screen.findByLabelText('消息输入')
  await user.type(input, '第二家安静吗')
  await user.click(screen.getByRole('button', { name: /发送/ }))

  expect(harness.sent).toEqual([{ threadId: 'thread-1', content: '第二家安静吗' }])
  await waitFor(() => expect(screen.getByLabelText('消息输入')).toBeDisabled())

  await act(async () => {
    stream().emit({ type: 'message.start', run_id: 'run-1', thread_id: 'thread-1' })
    stream().emit({ type: 'message.delta', delta: '结论：' })
  })
  await screen.findByText(/结论：/)

  await act(async () => {
    stream().emit({ type: 'message.delta', delta: '第二家是 Kiraku[^2]' })
    stream().emit({
      type: 'citation',
      evidence_ids: [2],
    })
    stream().emit({ type: 'message.end', finish_reason: 'stop' })
    stream().end()
  })

  await screen.findByText(/第二家是 Kiraku/)
  await waitFor(() => expect(screen.getByLabelText('消息输入')).toBeEnabled())
  // The citation set from the stream is what makes the new footnote live.
  expect(screen.getByRole('button', { name: '2' })).toBeEnabled()
})

test('tools and awaiting gates survive a message.end', async () => {
  const user = userEvent.setup()
  renderConsole()

  const input = await screen.findByLabelText('消息输入')
  await user.type(input, '第二家安静吗')
  await user.click(screen.getByRole('button', { name: /发送/ }))
  await waitFor(() => expect(harness.streams).toHaveLength(1))

  await act(async () => {
    stream().emit({ type: 'message.start', run_id: 'run-1', thread_id: 'thread-1' })
    stream().emit({ type: 'tool.start', call_id: 'call-a', tool: 'search_restaurants' })
    stream().emit({ type: 'tool.finish', call_id: 'call-a', status: 'ok', latency_ms: 842 })
    stream().emit({
      type: 'state.awaiting_input',
      state: 'awaiting_clarification',
      missing_slots: ['restaurant_id'],
    })
    stream().emit({ type: 'message.end', finish_reason: 'stop' })
    stream().end()
  })

  // The turn is parked on the user, not finished — the badge says so even though
  // the stream closed normally.
  await screen.findByText('等待你回答')
  const timeline = screen.getByTestId('tool-timeline')
  expect(within(timeline).getByText('search_restaurants')).toBeInTheDocument()
  expect(within(timeline).getByText('842ms')).toBeInTheDocument()
})

test('an error event marks the turn failed and keeps the text that arrived', async () => {
  const user = userEvent.setup()
  renderConsole()

  const input = await screen.findByLabelText('消息输入')
  await user.type(input, '会失败的一轮')
  await user.click(screen.getByRole('button', { name: /发送/ }))
  await waitFor(() => expect(harness.streams).toHaveLength(1))

  await act(async () => {
    stream().emit({ type: 'message.start', run_id: 'run-1', thread_id: 'thread-1' })
    stream().emit({ type: 'message.delta', delta: '已经收到的半句' })
    stream().emit({ type: 'error', code: 'model_error', message: 'provider EOF' })
    stream().end()
  })

  await screen.findByText('本轮回答失败')
  expect(screen.getByText('provider EOF')).toBeInTheDocument()
  expect(screen.getByText(/已经收到的半句/)).toBeInTheDocument()
})

test('a stream that closes with no message.end is reported, not left running', async () => {
  const user = userEvent.setup()
  renderConsole()

  const input = await screen.findByLabelText('消息输入')
  await user.type(input, '流会断')
  await user.click(screen.getByRole('button', { name: /发送/ }))
  await waitFor(() => expect(harness.streams).toHaveLength(1))

  await act(async () => {
    stream().emit({ type: 'message.start', run_id: 'run-1', thread_id: 'thread-1' })
    stream().emit({ type: 'message.delta', delta: '断在这里' })
    stream().end()
  })

  await screen.findByText(/本轮未正常结束/)
  await waitFor(() => expect(screen.getByLabelText('消息输入')).toBeEnabled())
})

test('warnings from message.end are surfaced, not swallowed', async () => {
  const user = userEvent.setup()
  renderConsole()

  const input = await screen.findByLabelText('消息输入')
  await user.type(input, '降级的一轮')
  await user.click(screen.getByRole('button', { name: /发送/ }))
  await waitFor(() => expect(harness.streams).toHaveLength(1))

  await act(async () => {
    stream().emit({ type: 'message.start', run_id: 'run-1', thread_id: 'thread-1' })
    stream().emit({
      type: 'message.end',
      finish_reason: 'stop',
      usage: { input_tokens: 1200, output_tokens: 300, total_tokens: 1500 },
      warnings: ['向量通道不可用'],
    })
    stream().end()
  })

  await screen.findByText('向量通道不可用')
  expect(screen.getByText(/1200↑/)).toBeInTheDocument()
})

test('the route renders the console under the shell', async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/agent']}>
        <App />
      </MemoryRouter>
    </QueryClientProvider>,
  )

  expect(await screen.findByRole('heading', { name: 'Agent Console' })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: /Agent Console/ })).toBeInTheDocument()
})

// The stream and the transcript describe the same turn. Rendering both would
// show the question and its answer twice; the echo is dropped by content,
// because the turn has no message id until the transcript has been re-read.
test('withoutEchoedTurn drops exactly the pair the stream is already showing', () => {
  const messages = [
    {
      message_id: 'm1',
      role: 'user' as const,
      content: '之前的',
      seq: 1,
      created_at: '2026-10-04T00:00:00Z',
    },
    {
      message_id: 'm2',
      role: 'user' as const,
      content: '第二家安静吗',
      seq: 2,
      created_at: '2026-10-04T00:00:01Z',
    },
    {
      message_id: 'm3',
      role: 'assistant' as const,
      content: '结论：安静。',
      seq: 3,
      created_at: '2026-10-04T00:00:02Z',
    },
  ]
  const turn = { text: '结论：安静。' } as Parameters<typeof withoutEchoedTurn>[2]

  expect(withoutEchoedTurn(messages, '第二家安静吗', turn).map((m) => m.message_id)).toEqual(['m1'])
  // A partial stream matches nothing, so nothing is hidden while it is arriving.
  expect(
    withoutEchoedTurn(messages, '第二家安静吗', { text: '结论：' } as typeof turn).map(
      (m) => m.message_id,
    ),
  ).toEqual(['m1', 'm2', 'm3'])
})

test('the await gate is the badge the thread list shows, not a second vocabulary', async () => {
  const user = userEvent.setup()
  renderConsole()

  const input = await screen.findByLabelText('消息输入')
  await user.type(input, '触发澄清')
  await user.click(screen.getByRole('button', { name: /发送/ }))
  await waitFor(() => expect(harness.streams).toHaveLength(1))

  await act(async () => {
    stream().emit({ type: 'message.start', run_id: 'run-1', thread_id: 'thread-1' })
    stream().emit({ type: 'state.awaiting_input', state: 'awaiting_clarification' })
    stream().emit({ type: 'message.end', finish_reason: 'stop' })
    stream().end()
  })

  // Clarification means the thread is still usable: the box to answer it in is
  // never disabled by the gate.
  await screen.findByText('等待你回答')
  await waitFor(() => expect(screen.getByLabelText('消息输入')).toBeEnabled())
})

test('the right rail keeps tools, citations and utilities on one screen', async () => {
  renderConsole()

  const rail = await screen.findByRole('tablist')
  expect(within(rail).getByRole('tab', { name: /工具链/ })).toBeInTheDocument()
  expect(within(rail).getByRole('tab', { name: /引用/ })).toBeInTheDocument()
  expect(within(rail).getByRole('tab', { name: /记忆/ })).toBeInTheDocument()
})

// The whole point of the citation layer: a number in the answer has to open the
// document it is standing on, and it has to ask for that document by id.
test('clicking a footnote opens the document it names', async () => {
  const user = userEvent.setup()
  renderConsole()

  await user.click(await screen.findByRole('button', { name: '1' }))

  expect(vi.mocked(chatApi.fetchEvidence)).toHaveBeenCalledWith({ evidence_ids: [1] })
  expect(await screen.findByText('店内安静，适合聊天。')).toBeInTheDocument()
  expect(screen.getByText('google_places')).toBeInTheDocument()
  expect(screen.getByText('2026-09-30 12:00:00Z')).toBeInTheDocument()
})

// Tools, candidates and the replayed run share one column because they answer
// one question: what actually happened during this answer. The same call shows
// twice on purpose — once on the live timeline, now enriched with the summary
// the stream could not carry, and once in the run it belongs to.
test('the tools tab carries the replayed summary and the candidate ordinals', async () => {
  renderConsole()

  const timeline = await screen.findByTestId('tool-timeline')
  expect(within(timeline).getByText('search_restaurants')).toBeInTheDocument()
  expect(within(timeline).getByText('找到 8 家')).toBeInTheDocument()
  expect(within(timeline).getByText('842ms')).toBeInTheDocument()

  const replay = await screen.findByTestId('run-replay-panel')
  expect(within(replay).getByText('找到 8 家')).toBeInTheDocument()

  const cards = await screen.findByTestId('candidate-cards')
  expect(within(cards).getByText('#1')).toBeInTheDocument()
  expect(within(cards).getByText('id 42')).toBeInTheDocument()
})

test('the citations tab lists the set the transcript can honour', async () => {
  const user = userEvent.setup()
  renderConsole()

  await user.click(await screen.findByRole('tab', { name: /引用/ }))
  expect(await screen.findByText('[^1]')).toBeInTheDocument()
})

// Confirmation is the one thing on this page that writes outside a turn, so the
// decision has to reach the endpoint verbatim and the server's own sentence
// about what happened has to come back.
test('a confirmation gate posts the decision that was clicked', async () => {
  const user = userEvent.setup()
  renderConsole()

  const input = await screen.findByLabelText('消息输入')
  await user.type(input, '帮我订 Tabetomo')
  await user.click(screen.getByRole('button', { name: /发送/ }))
  await waitFor(() => expect(harness.streams).toHaveLength(1))

  await act(async () => {
    stream().emit({ type: 'message.start', run_id: 'run-1', thread_id: 'thread-1' })
    stream().emit({
      type: 'confirmation.required',
      state: 'awaiting_confirmation',
      pending_action: 'request_reservation',
      summary: '准备为 Tabetomo 预约 2 人。',
    })
    stream().emit({ type: 'message.end', finish_reason: 'stop' })
    stream().end()
  })

  expect(await screen.findByText('准备为 Tabetomo 预约 2 人。')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: /确认/ }))

  await waitFor(() =>
    expect(vi.mocked(chatApi.confirm)).toHaveBeenCalledWith('thread-1', 'confirm'),
  )
  expect(await screen.findByText('已按你的选择处理。')).toBeInTheDocument()
})

test('a clarification gate keeps the input usable and does not print the slot name', async () => {
  const user = userEvent.setup()
  renderConsole()

  const input = await screen.findByLabelText('消息输入')
  await user.type(input, '第二家安静吗')
  await user.click(screen.getByRole('button', { name: /发送/ }))
  await waitFor(() => expect(harness.streams).toHaveLength(1))

  await act(async () => {
    stream().emit({ type: 'message.start', run_id: 'run-1', thread_id: 'thread-1' })
    stream().emit({
      type: 'state.awaiting_input',
      state: 'awaiting_clarification',
      missing_slots: ['restaurant_id'],
    })
    stream().emit({ type: 'message.end', finish_reason: 'stop' })
    stream().end()
  })

  const banner = await screen.findByTestId('gate-banner')
  expect(banner).toHaveTextContent('具体是哪一家店')
  expect(banner).not.toHaveTextContent('restaurant_id')
  // A clarification is a question, not a failure: the answer goes in the same box.
  await waitFor(() => expect(screen.getByLabelText('消息输入')).toBeEnabled())
})

// A memory is written behind the user's back, so the transcript says so and the
// panel that can correct it is one click away.
test('a memory event announces itself and links to the panel', async () => {
  const user = userEvent.setup()
  renderConsole()

  const input = await screen.findByLabelText('消息输入')
  await user.type(input, '记住我不吃辣')
  await user.click(screen.getByRole('button', { name: /发送/ }))
  await waitFor(() => expect(harness.streams).toHaveLength(1))

  await act(async () => {
    stream().emit({ type: 'message.start', run_id: 'run-1', thread_id: 'thread-1' })
    stream().emit({
      type: 'memory.saved',
      memory_id: 'mem-1',
      memory_type: 'dietary',
      content: '不吃辣',
    })
    stream().emit({ type: 'message.end', finish_reason: 'stop' })
    stream().end()
  })

  const notice = await screen.findByTestId('memory-notice-mem-1')
  expect(notice).toHaveTextContent('已记住：不吃辣')

  await user.click(within(notice).getByRole('button', { name: /查看/ }))
  const panel = await screen.findByTestId('memory-panel')
  expect(within(panel).getByText('不吃辣')).toBeInTheDocument()
})

test('a refreshed memory says updated, not remembered', async () => {
  const user = userEvent.setup()
  renderConsole()

  const input = await screen.findByLabelText('消息输入')
  await user.type(input, '我不吃辣')
  await user.click(screen.getByRole('button', { name: /发送/ }))
  await waitFor(() => expect(harness.streams).toHaveLength(1))

  await act(async () => {
    stream().emit({ type: 'message.start', run_id: 'run-1', thread_id: 'thread-1' })
    stream().emit({
      type: 'memory.saved',
      memory_id: 'mem-1',
      memory_type: 'dietary',
      content: '不吃辣，包括花椒',
      refreshed: true,
    })
    stream().emit({ type: 'message.end', finish_reason: 'stop' })
    stream().end()
  })

  expect(await screen.findByText(/已更新记忆/)).toBeInTheDocument()
})
