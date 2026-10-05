import { describe, expect, it } from 'vitest'
import {
  initialTurnState,
  reduce,
  unresolvedCitations,
  type TurnState,
} from './useAgentTurn'
import type { StreamEvent } from '../api/chat'

function run(events: StreamEvent[], from: TurnState = initialTurnState): TurnState {
  return events.reduce(reduce, from)
}

const delta = (delta: string): StreamEvent => ({ type: 'message.delta', delta })

describe('reduce', () => {
  it('accumulates text across deltas', () => {
    const state = run([delta('布鲁克林'), delta('的拉面')])
    expect(state.text).toBe('布鲁克林的拉面')
  })

  it('pairs tool.start and tool.finish by call_id, not by position', () => {
    // Two calls overlapping: the second one finishes first. Index pairing would
    // attach this result to the search.
    const state = run([
      { type: 'tool.start', call_id: 'a', tool: 'search_restaurants' },
      { type: 'tool.start', call_id: 'b', tool: 'get_restaurant_evidence' },
      { type: 'tool.finish', call_id: 'b', status: 'succeeded', latency_ms: 120 },
      { type: 'tool.finish', call_id: 'a', status: 'succeeded', latency_ms: 340 },
    ])
    expect(state.tools).toEqual([
      { call_id: 'a', tool: 'search_restaurants', status: 'succeeded', latency_ms: 340 },
      { call_id: 'b', tool: 'get_restaurant_evidence', status: 'succeeded', latency_ms: 120 },
    ])
  })

  it('keeps a tool running until its own finish arrives', () => {
    const state = run([
      { type: 'tool.start', call_id: 'a', tool: 'search_restaurants' },
      { type: 'tool.start', call_id: 'b', tool: 'get_restaurant_evidence' },
      { type: 'tool.finish', call_id: 'a', status: 'failed', latency_ms: 10 },
    ])
    expect(state.tools[1].status).toBe('running')
  })

  it('merges citation sets without duplicates', () => {
    const state = run([
      { type: 'citation', evidence_ids: [1, 2] },
      { type: 'citation', evidence_ids: [2, 3] },
    ])
    expect(state.citations).toEqual([1, 2, 3])
  })

  it('ends a clarification turn awaiting the user rather than done', () => {
    const state = run([
      { type: 'message.start', run_id: 'r1', thread_id: 't1' },
      delta('你想吃哪个菜系？'),
      {
        type: 'state.awaiting_input',
        state: 'awaiting_clarification',
        missing_slots: ['cuisine'],
      },
      { type: 'message.end', finish_reason: 'awaiting_input' },
    ])
    expect(state.phase).toBe('awaiting_user')
    expect(state.text).toBe('你想吃哪个菜系？')
    expect(state.awaiting?.missingSlots).toEqual(['cuisine'])
  })

  it('ends a confirmation turn awaiting a decision', () => {
    const state = run([
      { type: 'message.start', run_id: 'r1', thread_id: 't1' },
      {
        type: 'confirmation.required',
        state: 'awaiting_confirmation',
        pending_action: 'request_reservation',
        summary: '确认预约：Joe’s Pizza',
      },
      { type: 'message.end', finish_reason: 'awaiting_confirmation' },
    ])
    expect(state.phase).toBe('awaiting_confirm')
    expect(state.confirmation?.summary).toBe('确认预约：Joe’s Pizza')
  })

  it('records usage and warnings at the end of a turn', () => {
    const state = run([
      {
        type: 'message.end',
        finish_reason: 'stop',
        usage: { input_tokens: 10, output_tokens: 20, total_tokens: 30 },
        warnings: ['向量通道不可用'],
      },
    ])
    expect(state.phase).toBe('done')
    expect(state.usage?.total_tokens).toBe(30)
    expect(state.warnings).toEqual(['向量通道不可用'])
  })

  it('marks a failed turn but keeps the text already received', () => {
    const state = run([delta('半句'), { type: 'error', code: 'internal', message: '模型断了' }])
    expect(state.phase).toBe('failed')
    expect(state.text).toBe('半句')
    expect(state.error).toBe('模型断了')
  })

  it('separates a refreshed memory from a new one', () => {
    const state = run([
      { type: 'memory.saved', memory_id: 'm1', memory_type: 'preference', content: '不吃辣' },
      {
        type: 'memory.saved',
        memory_id: 'm1',
        memory_type: 'preference',
        content: '不吃辣，也不吃香菜',
        refreshed: true,
      },
    ])
    expect(state.memories).toHaveLength(1)
    expect(state.memories[0].refreshed).toBe(true)
    expect(state.memories[0].content).toBe('不吃辣，也不吃香菜')
  })

  it('replaces the whole run body when the server corrects the answer', () => {
    // Deltas are provisional: the server streams text before it has validated
    // the citations, and a violation is repaired by regenerating the answer.
    // Appending the correction would render the answer twice.
    const state = run([
      delta('结论：这家很安静[^999]。'),
      { type: 'message.replace', text: '结论：这家很安静[^1]。' },
      { type: 'citation', evidence_ids: [1] },
    ])
    expect(state.text).toBe('结论：这家很安静[^1]。')
    expect(state.citations).toEqual([1])
  })

  it('keeps the streamed text when no replacement arrives', () => {
    const state = run([delta('半句'), delta('，还有半句')])
    expect(state.text).toBe('半句，还有半句')
  })

  it('does not carry a replacement into the next turn', () => {
    const dirty = run([{ type: 'message.replace', text: '上一轮的校准正文' }])
    const fresh = run([{ type: 'message.start', run_id: 'r2', thread_id: 't1' }], dirty)
    expect(fresh.text).toBe('')
  })

  it('ignores an event it does not recognise', () => {
    // Node-level trace is planned for M6; a client that throws here would break
    // the whole turn the day the server starts emitting it.
    const before: TurnState = { ...initialTurnState, phase: 'streaming', text: '在写' }
    const after = reduce(before, { type: 'trace.node' } as unknown as StreamEvent)
    expect(after).toEqual(before)
  })

  it('resets accumulated state when a new turn starts', () => {
    const dirty = run([delta('旧一轮'), { type: 'citation', evidence_ids: [9] }])
    const fresh = run([{ type: 'message.start', run_id: 'r2', thread_id: 't1' }], dirty)
    expect(fresh.text).toBe('')
    expect(fresh.citations).toEqual([])
    expect(fresh.runId).toBe('r2')
  })
})

describe('unresolvedCitations', () => {
  it('reports footnotes the evidence set cannot back', () => {
    expect(unresolvedCitations('见 [^1] 和 [^7]', [1])).toEqual([7])
  })

  it('is empty when every footnote has evidence behind it', () => {
    expect(unresolvedCitations('见 [^1]', [1, 2])).toEqual([])
  })
})
