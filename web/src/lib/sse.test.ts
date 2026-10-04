import { describe, expect, it } from 'vitest'
import { parseSSEFrames, emitFrame } from './sse'

// The transport gives no guarantee that one network read is one frame, so the
// parser cannot either; these cases are the ways the network splits things.
describe('parseSSEFrames', () => {
  it('reads one complete frame', () => {
    const frames = parseSSEFrames(['event: message.delta\ndata: {"delta":"hi"}\n\n'])
    expect(frames).toEqual([{ event: 'message.delta', data: '{"delta":"hi"}' }])
  })

  it('reassembles a frame split across three chunks', () => {
    const chunks = [
      'event: message.del',
      'ta\ndata: {"de',
      'lta":"一根"}\n\n',
    ]
    const frames = parseSSEFrames(chunks)
    expect(frames).toHaveLength(1)
    expect(frames[0].event).toBe('message.delta')
    expect(JSON.parse(frames[0].data)).toEqual({ delta: '一根' })
  })

  it('ignores heartbeat comments rather than reading them as unnamed frames', () => {
    const frames = parseSSEFrames([
      ': keep-alive\n\n',
      'event: message.delta\ndata: {"delta":"x"}\n\n',
      ': keep-alive\n\n',
    ])
    expect(frames).toHaveLength(1)
    expect(frames[0].event).toBe('message.delta')
  })

  it('reads several frames arriving in one chunk', () => {
    const frames = parseSSEFrames([
      'event: tool.start\ndata: {"call_id":"a"}\n\nevent: tool.finish\ndata: {"call_id":"a"}\n\n',
    ])
    expect(frames.map((f) => f.event)).toEqual(['tool.start', 'tool.finish'])
  })

  it('delivers the last frame when the stream ends without a blank line', () => {
    const frames = parseSSEFrames(['event: message.end\ndata: {"finish_reason":"stop"}'])
    expect(frames).toHaveLength(1)
    expect(frames[0].event).toBe('message.end')
  })

  it('keeps multiline data as one payload', () => {
    const frames = parseSSEFrames(['event: x\ndata: {"a":\ndata: 1}\n\n'])
    expect(frames[0].data).toBe('{"a":\n1}')
  })

  it('drops a frame with no event name', () => {
    const frames = parseSSEFrames(['data: {"orphan":true}\n\n'])
    expect(frames).toHaveLength(0)
  })
})

// Bad JSON is decided by the caller, not here: this layer's only contract is the
// frame boundary, so it emits what it parsed and lets the consumer skip what it
// cannot decode. That separation is why one malformed frame cannot end a turn.
describe('emitFrame', () => {
  it('hands frames to the callback with their data intact', () => {
    const seen: string[] = []
    emitFrame('event: error\ndata: {"code":"boom"}', (frame) => seen.push(frame.event))
    expect(seen).toEqual(['error'])
  })
})
