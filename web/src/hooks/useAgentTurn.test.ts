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

  it('keeps reasoning on its own channel, never in the answer', () => {
    // The two are different things: reasoning is a cue that the model is
    // working, and appending it to the answer would put scratch work in the
    // transcript the user keeps.
    const state = run([
      { type: 'thinking.delta', delta: '先看证据' },
      { type: 'thinking.delta', delta: '，再下结论' },
      delta('结论：A 更好'),
    ])
    expect(state.thinking).toBe('先看证据，再下结论')
    expect(state.text).toBe('结论：A 更好')
  })

  it('clears the previous turn\'s reasoning on message.start', () => {
    const previous = run([{ type: 'thinking.delta', delta: '上一轮的思考' }])
    const state = reduce(previous, { type: 'message.start', run_id: 'r2', thread_id: 't1' })
    expect(state.thinking).toBe('')
    expect(state.text).toBe('')
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

  it('appends a phase in the order it opened', () => {
    // Phases arrive in the order the runner executed them, and the list the
    // console renders is the order the user watched them happen.
    const state = run([
      {
        type: 'phase.started',
        run_id: 'r1',
        thread_id: 't1',
        phase: 'ingress',
        phase_id: 'ingress-1',
        title: '正在加载会话上下文',
        started_at: 1000,
      },
      {
        type: 'phase.started',
        run_id: 'r1',
        thread_id: 't1',
        phase: 'plan',
        phase_id: 'plan-1',
        title: '正在制定下一步计划',
        started_at: 1100,
      },
    ])
    expect(state.phases.map((p) => p.phase)).toEqual(['ingress', 'plan'])
    expect(state.phases.every((p) => p.status === 'running')).toBe(true)
  })

  it('closes the right phase by phase_id, not by name', () => {
    // A tools round that loops back to plan would replace the first round's
    // row if pairing were by phase name. phase_id is what makes the second
    // round its own row.
    const state = run([
      {
        type: 'phase.started',
        run_id: 'r1',
        thread_id: 't1',
        phase: 'plan',
        phase_id: 'plan-1',
        title: '正在制定下一步计划',
        started_at: 1100,
      },
      {
        type: 'phase.started',
        run_id: 'r1',
        thread_id: 't1',
        phase: 'plan',
        phase_id: 'plan-2',
        title: '正在制定下一步计划',
        started_at: 1500,
      },
      {
        type: 'phase.finished',
        run_id: 'r1',
        thread_id: 't1',
        phase: 'plan',
        phase_id: 'plan-1',
        finished_at: 1500,
        outcome: 'ok',
      },
    ])
    expect(state.phases[0].status).toBe('ok')
    expect(state.phases[1].status).toBe('running')
  })

  it('nests a step under its parent phase', () => {
    const state = run([
      {
        type: 'phase.started',
        run_id: 'r1',
        thread_id: 't1',
        phase: 'ingress',
        phase_id: 'ingress-1',
        title: '正在加载会话上下文',
        started_at: 1000,
      },
      {
        type: 'step.started',
        run_id: 'r1',
        thread_id: 't1',
        phase: 'ingress',
        phase_id: 'ingress-1',
        step: 'embedding_memory',
        step_id: 'ingress-1-embedding_memory',
        title: '检索长期记忆',
        started_at: 1050,
      },
      {
        type: 'step.finished',
        run_id: 'r1',
        thread_id: 't1',
        phase: 'ingress',
        phase_id: 'ingress-1',
        step: 'embedding_memory',
        step_id: 'ingress-1-embedding_memory',
        finished_at: 1080,
        outcome: 'ok',
      },
    ])
    const ingress = state.phases[0]
    expect(ingress.steps).toHaveLength(1)
    expect(ingress.steps[0].step).toBe('embedding_memory')
    expect(ingress.steps[0].status).toBe('ok')
  })

  it('marks a phase failed without taking the turn down', () => {
    // A failed phase is the runner reporting a node error; the run is
    // already over. The console shows red, not a redirect to the error
    // page.
    const state = run([
      {
        type: 'phase.started',
        run_id: 'r1',
        thread_id: 't1',
        phase: 'answer',
        phase_id: 'answer-1',
        title: '正在生成回答',
        started_at: 2000,
      },
      {
        type: 'phase.finished',
        run_id: 'r1',
        thread_id: 't1',
        phase: 'answer',
        phase_id: 'answer-1',
        finished_at: 2010,
        outcome: 'failed',
      },
    ])
    expect(state.phases[0].status).toBe('failed')
  })

  it('keeps phases collected before message.start', () => {
    // A late message.start (the runner emits it before phase.started
    // today, but a deployment that has not yet rolled that change sends
    // it after) must not wipe a phase row the front-end is already
    // rendering: the timeline would silently disappear.
    const dirty = run([
      {
        type: 'phase.started',
        run_id: 'r1',
        thread_id: 't1',
        phase: 'plan',
        phase_id: 'plan-1',
        title: '正在制定下一步计划',
        started_at: 1100,
      },
    ])
    const fresh = run([{ type: 'message.start', run_id: 'r2', thread_id: 't1' }], dirty)
    expect(fresh.phases).toHaveLength(1)
    expect(fresh.text).toBe('')
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
