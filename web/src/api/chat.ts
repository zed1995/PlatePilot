// chat.ts is the conversational surface: everything under /v1 that belongs to a
// user rather than to an operator.
//
// It is a separate module from client.ts on purpose. The console talks to
// /admin/v1 as nobody and never writes; this one talks to /v1 as somebody and
// does write. Folding them together would make every adminApi call carry a user
// id it does not have.
import type {
  CandidateListResponse,
  ConfirmResponse,
  EvidenceBundle,
  EvidenceQuery,
  InterpretResult,
  MemoryListResponse,
  MemoryView,
  MessageListResponse,
  RunDetail,
  RunListResponse,
  SearchRequest,
  SearchResponse,
  Thread,
  ThreadListResponse,
} from './types'
import { ApiError, type ErrorEnvelope } from './client'
import { readSSEStream } from '../lib/sse'

// Empty by default: in development Vite proxies /v1 to the local service.
const base = import.meta.env.VITE_API_BASE ?? ''

// currentUserId is the placeholder identity every request carries.
//
// M6 replaces X-User-ID with a real principal, and when it does this module
// state is the only thing that changes — no page reads the header directly. It
// starts as demo-user rather than empty because an anonymous thread silently
// skips memory injection, which is a different product from the one the console
// is meant to show.
let currentUserId = 'demo-user'

export function getUserId(): string {
  return currentUserId
}

export function setUserId(id: string): void {
  currentUserId = id.trim() || 'demo-user'
}

async function request<T>(opts: {
  method: string
  path: string
  body?: unknown
}): Promise<T> {
  const headers: Record<string, string> = { 'X-User-ID': currentUserId }
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'

  const response = await fetch(base + opts.path, {
    method: opts.method,
    headers,
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
  })

  if (response.status === 204) {
    // No content is a successful answer, not a body waiting to be parsed: DELETE
    // replies with nothing, and decoding that would throw on every delete.
    return undefined as T
  }

  const data = (await response.json().catch(() => null)) as (T & ErrorEnvelope) | null
  if (!response.ok) {
    const envelope = data?.error
    throw new ApiError(
      response.status,
      envelope?.code ?? 'unknown',
      envelope?.message ?? `request failed with status ${response.status}`,
      envelope?.request_id,
    )
  }
  return data as T
}

const get = <T>(path: string): Promise<T> => request<T>({ method: 'GET', path })
const post = <T>(path: string, body?: unknown): Promise<T> =>
  request<T>({ method: 'POST', path, body })

export interface ThreadsParams {
  limit?: number
  before_id?: string
}

export interface MessagesParams {
  limit?: number
  before_id?: string
}

export interface RunsParams {
  limit?: number
  before_id?: string
}

function query(params: object): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== '') search.set(key, String(value))
  }
  const encoded = search.toString()
  return encoded ? `?${encoded}` : ''
}

// ---------------------------------------------------------------------------
// The streaming turn
// ---------------------------------------------------------------------------

// StreamEvent is one frame's decoded payload.
//
// Unknown event names are absent from this union deliberately. The back end's
// event set is closed today but grows as the agent does — node-level trace is
// already planned — and a client that rejected an unfamiliar frame would break
// every time the server learned something new. Callers must treat an unmatched
// name as "ignore", and the union is what makes that the default in TypeScript.
export type StreamEvent =
  | { type: 'message.start'; run_id: string; thread_id: string }
  | { type: 'message.delta'; delta: string }
  // thinking.delta is one increment of the model's own reasoning. It is a
  // channel of its own — never appended to the answer — because on a
  // reasoning model it is the only output for the long stretch before the
  // first answer token, and rendering it as "思考中" is what makes that
  // stretch visibly alive instead of a silent spinner.
  | { type: 'thinking.delta'; delta: string }
  // The corrected answer body. Deltas are provisional — the server publishes
  // text as it is generated, and a citation that turns out to be out of range
  // is repaired afterwards — so a replacement means "discard this run's text
  // and keep this instead", not "append this".
  | { type: 'message.replace'; text: string }
  | { type: 'tool.start'; call_id: string; tool: string }
  | { type: 'tool.finish'; call_id: string; status: string; latency_ms: number }
  | { type: 'citation'; evidence_ids: number[] }
  | {
      type: 'state.awaiting_input'
      state: string
      pending_action?: string
      missing_slots?: string[]
    }
  | { type: 'confirmation.required'; state: string; pending_action: string; summary: string }
  | {
      type: 'memory.saved'
      memory_id: string
      memory_type: string
      content: string
      refreshed?: boolean
    }
  | {
      type: 'message.end'
      finish_reason: string
      usage?: { input_tokens: number; output_tokens: number; total_tokens: number }
      warnings?: string[]
    }
  | { type: 'error'; code: string; message: string }
  // phase.progress rewrites the title of the running phase whose id matches
  // phase_id. It exists so a long-running plan call can show "正在推理
  // （已 N 秒）" instead of staying on "正在制定下一步计划" for the whole
  // duration; the row is matched by id, never by phase name.
  | { type: 'phase.progress'; phase_id: string; title: string }
  // Phase / step events carry the lifecycle of an in-flight turn. A blank
  // "运行中" badge is not a status — it is the visible failure mode of a turn
  // that takes two minutes to plan but never produces a byte of its own. The
  // server emits phase.started / phase.finished around each of the runner's
  // coarse phases (ingress / plan / tools / answer) so the console can show
  // which step is in flight, and step.started / step.finished for the three
  // sub-actions of ingress (loading_context, embedding_memory, interpreting)
  // so the loading phase itself is not one big unnamed wait.
  | {
      type: 'phase.started'
      run_id: string
      thread_id: string
      phase: 'ingress' | 'plan' | 'tools' | 'answer'
      phase_id: string
      title: string
      started_at: number
    }
  | {
      type: 'phase.finished'
      run_id: string
      thread_id: string
      phase: 'ingress' | 'plan' | 'tools' | 'answer'
      phase_id: string
      finished_at: number
      outcome?: 'ok' | 'failed'
    }
  | {
      type: 'step.started'
      run_id: string
      thread_id: string
      phase: 'ingress'
      phase_id: string
      step: 'loading_context' | 'embedding_memory' | 'interpreting'
      step_id: string
      title: string
      started_at: number
    }
  | {
      type: 'step.finished'
      run_id: string
      thread_id: string
      phase: 'ingress'
      phase_id: string
      step: 'loading_context' | 'embedding_memory' | 'interpreting'
      step_id: string
      finished_at: number
      outcome?: 'ok' | 'failed'
    }

export interface SendMessageHandle {
  abort: () => void
  done: Promise<void>
}

// sendMessage opens one turn and returns the stream's own lifecycle.
//
// A turn is a process rather than a result, so there is nothing to await for an
// answer — the caller subscribes and gets a handle it can stop. Stopping aborts
// the fetch, which is what cancels the run server-side: without it the model
// keeps spending for an audience that left.
//
// There is no retry here, and that is deliberate. A turn costs a model call and
// up to two minutes, and tool calls have effects, so re-running one after a
// timeout would double the cost of a request that may well have succeeded.
export function sendMessage(opts: {
  threadId: string
  content: string
  onEvent: (event: StreamEvent) => void
  onError?: (message: string) => void
}): SendMessageHandle {
  const control = new AbortController()
  const path = `/v1/conversations/${encodeURIComponent(opts.threadId)}/messages`

  const done = (async () => {
    let response: Response
    try {
      response = await fetch(base + path, {
        method: 'POST',
        headers: {
          'X-User-ID': currentUserId,
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ content: opts.content }),
        signal: control.signal,
      })
    } catch (err) {
      // An abort is the Stop button doing its job, not a failure worth showing.
      if (!control.signal.aborted) opts.onError?.(errorText(err))
      return
    }
    if (!response.ok) {
      // Before the first byte the server still answers with the normal JSON
      // envelope: a mistyped thread is a 404 body, not a stream.
      const data = (await response.json().catch(() => null)) as ErrorEnvelope | null
      const envelope = data?.error
      opts.onError?.(
        new ApiError(
          response.status,
          envelope?.code ?? 'unknown',
          envelope?.message ?? `request failed with status ${response.status}`,
          envelope?.request_id,
        ).message,
      )
      return
    }
    if (!response.body) {
      opts.onError?.('the stream had no body')
      return
    }
    await readSSEStream(response.body, (frame) => {
      let payload: unknown
      try {
        payload = JSON.parse(frame.data)
      } catch {
        // A frame whose data is not JSON is skipped rather than failing the
        // turn: one malformed frame must not cost the user the answer around it.
        return
      }
      if (typeof payload !== 'object' || payload === null) return
      // The event name is the type. The back end puts it on the SSE `event:`
      // line and never inside the JSON body, so the two halves have to be
      // recombined here: a reducer that switches on `event.type` reads
      // `undefined` for every frame otherwise, and the entire stream reduces
      // to a no-op — nothing renders until the turn ends and the transcript is
      // re-read.
      opts.onEvent({ ...(payload as Record<string, unknown>), type: frame.event } as StreamEvent)
    })
  })()

  return { abort: () => control.abort(), done }
}

// streamError is the message a transport failure or an aborted turn should show.
function errorText(err: unknown): string {
  if (err instanceof Error) {
    if (err.name === 'AbortError') return '已停止本轮回答。'
    return err.message
  }
  return String(err)
}

// ---------------------------------------------------------------------------
// The request surface
// ---------------------------------------------------------------------------

export const chatApi = {
  createThread: (title?: string) =>
    post<Thread>('/v1/conversations', title ? { title } : undefined),
  listThreads: (params: ThreadsParams = {}) =>
    get<ThreadListResponse>(`/v1/conversations${query(params)}`),
  getThread: (threadId: string) => get<Thread>(`/v1/conversations/${encodeURIComponent(threadId)}`),
  // Deletion answers 204 with no body, which is why `request` returns early on
  // that status rather than trying to parse one.
  deleteThread: (threadId: string) =>
    request<void>({ method: 'DELETE', path: `/v1/conversations/${encodeURIComponent(threadId)}` }),

  listMessages: (threadId: string, params: MessagesParams = {}) =>
    get<MessageListResponse>(
      `/v1/conversations/${encodeURIComponent(threadId)}/messages${query(params)}`,
    ),
  listCandidates: (threadId: string) =>
    get<CandidateListResponse>(`/v1/conversations/${encodeURIComponent(threadId)}/candidates`),

  confirm: (threadId: string, decision: 'confirm' | 'cancel') =>
    post<ConfirmResponse>(`/v1/conversations/${encodeURIComponent(threadId)}/confirm`, {
      decision,
    }),

  listMemories: () => get<MemoryListResponse>('/v1/memories'),
  updateMemory: (memoryId: string, body: { content?: string; memory_type?: string }) =>
    request<MemoryView>({
      method: 'PATCH',
      path: `/v1/memories/${encodeURIComponent(memoryId)}`,
      body,
    }),
  deleteMemory: (memoryId: string) =>
    request<void>({ method: 'DELETE', path: `/v1/memories/${encodeURIComponent(memoryId)}` }),

  listRuns: (threadId: string, params: RunsParams = {}) =>
    get<RunListResponse>(`/v1/conversations/${encodeURIComponent(threadId)}/runs${query(params)}`),
  getRun: (runId: string) => get<RunDetail>(`/v1/runs/${encodeURIComponent(runId)}`),

  searchRestaurants: (body: SearchRequest) => post<SearchResponse>('/v1/restaurants/search', body),
  fetchEvidence: (body: EvidenceQuery) => post<EvidenceBundle>('/v1/restaurants/evidence', body),
  interpret: (text: string) => post<InterpretResult>('/v1/restaurants/interpret', { text }),
}
