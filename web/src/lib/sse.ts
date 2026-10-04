// sse.ts parses one Server-Sent Events body into frames.
//
// The built-in EventSource cannot be used for this stream: the turn endpoint is
// a POST that carries an identity header, and EventSource supports neither. This
// is a fetch + ReadableStream reader instead, which is also the only option that
// can be cancelled, and cancelling is how a Stop button reaches the server.
export interface SSEFrame {
  event: string
  data: string
}

// readSSEStream consumes one response body and emits each frame it contains.
//
// The accumulating buffer is not defensive: a single SSE frame arrives as
// whatever TCP handed us, so assuming one read is one frame would interleave a
// message.delta with a frame boundary and hand JSON.parse half an object. The
// buffer is what makes the frame the unit again.
//
// Frames that carry no event name are dropped rather than emitted, which is what
// disposes of the back end's 15s ': keep-alive' comment frames — a comment has
// no event line and would otherwise read as an unnamed event.
export async function readSSEStream(
  body: ReadableStream<Uint8Array>,
  onFrame: (frame: SSEFrame) => void,
): Promise<void> {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })

    let boundary: number
    while ((boundary = buffer.indexOf('\n\n')) !== -1) {
      const frame = buffer.slice(0, boundary)
      buffer = buffer.slice(boundary + 2)
      emitFrame(frame, onFrame)
    }
  }
  // A stream that closed without a trailing blank line still owes its last
  // frame, and that frame is often the message.end a turn ended on.
  if (buffer.trim() !== '') emitFrame(buffer, onFrame)
}

// emitFrame turns one raw frame into an {event, data} pair, when it names an
// event at all.
export function emitFrame(raw: string, onFrame: (frame: SSEFrame) => void): void {
  let event = ''
  let data = ''
  for (const line of raw.split('\n')) {
    const trimmed = line.trimEnd()
    if (trimmed === '' || trimmed.startsWith(':')) continue
    if (trimmed.startsWith('event:')) {
      event = trimmed.slice(6).trim()
    } else if (trimmed.startsWith('data:')) {
      const payload = trimmed.slice(5).trim()
      data = data === '' ? payload : `${data}\n${payload}`
    }
  }
  if (event === '') return
  onFrame({ event, data })
}

// parseSSEFrames is readSSEStream with no I/O, for tests and for the unit a
// caller already has text for. It feeds one chunk at a time so a test can split
// a frame the way the network does.
export function parseSSEFrames(chunks: string[]): SSEFrame[] {
  const out: SSEFrame[] = []
  let buffer = ''
  const flush = () => {
    emitFrame(buffer, (frame) => out.push(frame))
    buffer = ''
  }
  for (const chunk of chunks) {
    buffer += chunk
    let boundary: number
    while ((boundary = buffer.indexOf('\n\n')) !== -1) {
      const frame = buffer.slice(0, boundary)
      buffer = buffer.slice(boundary + 2)
      emitFrame(frame, (f) => out.push(f))
    }
  }
  if (buffer.trim() !== '') flush()
  return out
}
