import { afterEach, describe, expect, it, vi } from 'vitest'

import { sendMessage, type StreamEvent } from './chat'

// sendMessage is the seam where an SSE frame's event name has to become the
// decoded event's `type`. Every other test in this app injects a fake
// sendMessage, or drives reduce() with objects that already carry a type, so a
// regression at this seam is invisible to all of them — and its failure mode is
// silent: reduce()'s `switch (event.type)` reads `undefined`, falls through to
// `default`, and drops the whole stream. Nothing renders until the turn ends
// and the transcript is re-read.
const frames = [
  'event: message.start\ndata: {"run_id":"r1","thread_id":"t1"}',
  'event: phase.started\ndata: {"phase_id":"ingress-1","phase":"ingress","title":"正在加载会话上下文","started_at":1}',
  'event: phase.progress\ndata: {"phase_id":"plan-1","title":"正在推理（已 3 秒）"}',
  'event: message.delta\ndata: {"delta":"你好"}',
  'event: message.end\ndata: {"finish_reason":"stop"}',
]

// streamOf hands the reader one chunk per frame, so the test exercises the
// frame parser too rather than a single prefetched blob.
function streamOf(chunks: string[]): Response {
  const encoder = new TextEncoder()
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk + '\n\n'))
      controller.close()
    },
  })
  return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
}

async function collect(response: Response): Promise<StreamEvent[]> {
  vi.stubGlobal('fetch', vi.fn(async () => response))
  const events: StreamEvent[] = []
  await sendMessage({ threadId: 't1', content: 'hi', onEvent: (e) => events.push(e) }).done
  return events
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('sendMessage', () => {
  it('carries the SSE event name onto the decoded event as `type`', async () => {
    const events = await collect(streamOf(frames))

    expect(events.map((e) => e.type)).toEqual([
      'message.start',
      'phase.started',
      'phase.progress',
      'message.delta',
      'message.end',
    ])
  })

  it('keeps the payload fields alongside the type', async () => {
    const events = await collect(streamOf(frames))

    const phase = events[1] as Extract<StreamEvent, { type: 'phase.started' }>
    expect(phase.phase_id).toBe('ingress-1')
    expect(phase.title).toBe('正在加载会话上下文')
  })

  it('skips a frame whose data is not JSON instead of ending the turn', async () => {
    const events = await collect(streamOf(['event: message.delta\ndata: not-json']))
    const more = await collect(streamOf(['event: message.delta\ndata: {"delta":"ok"}']))

    expect(events).toEqual([])
    expect(more).toEqual([{ type: 'message.delta', delta: 'ok' }])
  })
})
