import { render, screen } from '@testing-library/react'

import type { ToolCallView } from '../../api/types'
import { ToolTimeline, mergeToolCalls } from './tool-timeline'

function live(call_id: string, tool: string, status = 'running', latency_ms?: number) {
  return { call_id, tool, status, latency_ms }
}

function replayed(call_id: string, tool_name: string, extra: Partial<ToolCallView> = {}): ToolCallView {
  return {
    call_id,
    tool_name,
    status: 'ok',
    created_at: '2026-10-04T00:00:00Z',
    ...extra,
  }
}

// The two halves of the timeline arrive in different orders and are joined on
// call_id. Position pairing would attach one tool's result to another the moment
// two calls overlap, which is the normal case: the agent fans out.
test('mergeToolCalls pairs by call_id, not by position', () => {
  const merged = mergeToolCalls(
    [live('call-a', 'search_restaurants', 'ok', 120), live('call-b', 'get_restaurant_evidence', 'ok', 340)],
    [
      replayed('call-b', 'get_restaurant_evidence', { result_summary: '4 documents' }),
      replayed('call-a', 'search_restaurants', { result_summary: '8 candidates' }),
    ],
  )

  expect(merged.map((call) => [call.call_id, call.result_summary])).toEqual([
    ['call-a', '8 candidates'],
    ['call-b', '4 documents'],
  ])
})

test('mergeToolCalls keeps the order the user watched', () => {
  const merged = mergeToolCalls(
    [live('call-b', 'second', 'ok', 10), live('call-a', 'first', 'ok', 20)],
    [replayed('call-a', 'first'), replayed('call-b', 'second')],
  )
  expect(merged.map((call) => call.call_id)).toEqual(['call-b', 'call-a'])
})

// A call the stream never announced is exactly the kind of gap this page exists
// to make visible, so the replay is allowed to add rows.
test('mergeToolCalls appends a call only the replay knows about', () => {
  const merged = mergeToolCalls(
    [live('call-a', 'search_restaurants', 'ok', 120)],
    [
      replayed('call-a', 'search_restaurants'),
      replayed('call-z', 'request_reservation', { status: 'failed' }),
    ],
  )
  expect(merged.map((call) => call.call_id)).toEqual(['call-a', 'call-z'])
  expect(merged[1].status).toBe('failed')
})

test('mergeToolCalls without a replay is the live timeline unchanged', () => {
  const merged = mergeToolCalls([live('call-a', 'search_restaurants', 'running')], undefined)
  expect(merged).toEqual([
    { call_id: 'call-a', tool: 'search_restaurants', status: 'running', latency_ms: undefined, result_summary: undefined },
  ])
})

test('a running tool shows as running, not as a failure', () => {
  render(<ToolTimeline calls={mergeToolCalls([live('call-a', 'search_restaurants')], undefined)} />)
  expect(screen.getByText('search_restaurants')).toBeInTheDocument()
  expect(screen.getByText('running')).toBeInTheDocument()
})

// The same tool twice is two rows. Collapsing them into a count would answer
// "did it run" with a number when the question is "how many times did it run".
test('repeated calls to one tool render as separate rows, with the replay summary attached', () => {
  render(
    <ToolTimeline
      calls={mergeToolCalls(
        [live('call-a', 'search_restaurants', 'ok', 120), live('call-b', 'search_restaurants', 'ok', 880)],
        [
          replayed('call-b', 'search_restaurants', { result_summary: '第二次找到 6 家' }),
          replayed('call-a', 'search_restaurants', { result_summary: '第一次找到 8 家' }),
        ],
      )}
    />,
  )

  expect(screen.getAllByText('search_restaurants')).toHaveLength(2)
  expect(screen.getByText('第一次找到 8 家')).toBeInTheDocument()
  expect(screen.getByText('第二次找到 6 家')).toBeInTheDocument()
  expect(screen.getByText('120ms')).toBeInTheDocument()
})

test('an empty timeline says so instead of rendering nothing', () => {
  render(<ToolTimeline calls={[]} />)
  expect(screen.getByText('还没有工具调用')).toBeInTheDocument()
})
