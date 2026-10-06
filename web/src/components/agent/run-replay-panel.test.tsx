import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import type { RunDetail, RunView } from '../../api/types'
import { RunReplayPanel } from './run-replay-panel'

const run: RunView = {
  run_id: 'run-1',
  thread_id: 'thread-1',
  status: 'ok',
  model_name: 'qwen3-max',
  started_at: '2026-10-04T00:00:00Z',
  latency_ms: 91_000,
  tool_call_count: 2,
}

const detail: RunDetail = {
  ...run,
  tool_calls: [
    {
      call_id: 'call-a',
      tool_name: 'search_restaurants',
      status: 'ok',
      latency_ms: 1200,
      result_summary: '8 candidates',
      arguments: { borough: 'Brooklyn', min_rating: 4 },
      created_at: '2026-10-04T00:00:01Z',
    },
    {
      call_id: 'call-b',
      tool_name: 'get_restaurant_evidence',
      status: 'ok',
      latency_ms: 900,
      created_at: '2026-10-04T00:00:02Z',
    },
  ],
}

test('renders the run list and the selected run tool chain', () => {
  render(
    <RunReplayPanel runs={[run]} selectedRunId="run-1" onSelect={vi.fn()} detail={detail} />,
  )

  expect(screen.getByText('run-1')).toBeInTheDocument()
  expect(screen.getByText('qwen3-max')).toBeInTheDocument()
  expect(screen.getByText('8 candidates')).toBeInTheDocument()
  expect(screen.getByText('search_restaurants')).toBeInTheDocument()
  expect(screen.getByText('get_restaurant_evidence')).toBeInTheDocument()
})

// arguments arrive redacted and are shown as they came. Summarising them here
// would be a second description of what the agent sent, and the second one is
// the one that can be wrong.
test('redacted arguments are shown verbatim, folded', () => {
  render(<RunReplayPanel runs={[run]} selectedRunId="run-1" onSelect={vi.fn()} detail={detail} />)

  const disclosure = screen.getByText(/arguments/)
  expect(disclosure.tagName.toLowerCase()).toBe('summary')
  expect(screen.getByText(/"min_rating": 4/)).toBeInTheDocument()
})

test('picking another run reports the id', async () => {
  const user = userEvent.setup()
  const onSelect = vi.fn()
  const other: RunView = { ...run, run_id: 'run-2', status: 'failed' }
  render(<RunReplayPanel runs={[run, other]} selectedRunId="run-1" onSelect={onSelect} />)

  await user.click(screen.getByText('run-2'))
  expect(onSelect).toHaveBeenCalledWith('run-2')
})

test('no runs falls back to the empty state', () => {
  render(<RunReplayPanel runs={[]} selectedRunId={null} onSelect={vi.fn()} />)
  expect(screen.getByText('还没有 run')).toBeInTheDocument()
})

test('a run with no tools still opens', () => {
  render(
    <RunReplayPanel
      runs={[run]}
      selectedRunId="run-1"
      onSelect={vi.fn()}
      detail={{ ...detail, tool_calls: [] }}
    />,
  )
  expect(screen.getByText('这一轮没有调用工具')).toBeInTheDocument()
})

test('a failed run list read shows the error state', () => {
  render(<RunReplayPanel runs={[]} selectedRunId={null} onSelect={vi.fn()} error={new Error('boom')} />)
  expect(screen.getByText('run 列表加载失败')).toBeInTheDocument()
  expect(screen.getByText('boom')).toBeInTheDocument()
})

// The run row carries the upstream message beside the code, because the code
// only classifies a failure: the text is what names the model that broke.
test('a failed run shows its error message', () => {
  const failed: RunView = {
    ...run,
    status: 'failed',
    error_code: 'provider_unavailable',
    error_message: 'No endpoints found for stealth/space-bunny-alpha',
  }
  render(<RunReplayPanel runs={[failed]} selectedRunId={null} onSelect={vi.fn()} />)

  expect(
    screen.getByText('失败原因：No endpoints found for stealth/space-bunny-alpha'),
  ).toBeInTheDocument()
})

test('a run without an error message shows none', () => {
  render(<RunReplayPanel runs={[run]} selectedRunId={null} onSelect={vi.fn()} />)
  expect(screen.queryByText(/失败原因/)).not.toBeInTheDocument()
})
