// useAgentTurn reduces one turn's event stream into something renderable.
//
// Everything else on the page is presentation; this is the only logic there is,
// and its job is to be a faithful projection of the server's state machine. Two
// rules drive the shape:
//
//  1. tool.start and tool.finish pair on call_id, never on array position — a
//     concurrent tool call reorders the frames and position would mismatch.
//  2. awaiting_input and confirmation.required are not terminal states. Both are
//     followed by a message.end, and the turn has to end there rather than in
//     `done`, because the thread really is waiting and the whole point of the
//     badge next to it is that the server agrees.
//  3. message.delta is provisional for the whole run. The server streams text as
//     it is generated and validates citations afterwards, so a run's text is not
//     final until message.end; a message.replace supersedes everything streamed
//     before it. Nothing here may be written to a store mid-run.
import { useCallback, useEffect, useRef, useState } from 'react'
import { sendMessage, type StreamEvent } from '../api/chat'
import { collectCitationIds } from '../lib/answer'

export type TurnPhase =
  | 'idle'
  | 'streaming'
  | 'done'
  | 'failed'
  | 'awaiting_user'
  | 'awaiting_confirm'
  | 'canceled'
  // interrupted is the state a turn lands in when the stream closes with no
  // message.end and no error. The model provider does this occasionally; without
  // it the UI would sit on "running" forever for a turn that is already over.
  | 'interrupted'

export interface ToolCallItem {
  call_id: string
  tool: string
  status: 'running' | string
  latency_ms?: number
}

export interface MemorySavedItem {
  memoryId: string
  memoryType: string
  content: string
  refreshed: boolean
}

export interface Gate {
  kind: 'clarification' | 'confirmation'
  state: string
  pendingAction?: string
  missingSlots?: string[]
  summary?: string
}

export interface TurnState {
  phase: TurnPhase
  runId?: string
  threadId?: string
  text: string
  tools: ToolCallItem[]
  citations: number[]
  confirmation?: Gate
  awaiting?: Gate
  memories: MemorySavedItem[]
  finishReason?: string
  usage?: { input_tokens: number; output_tokens: number; total_tokens: number }
  warnings: string[]
  error?: string
  startedAt?: number
  elapsedMs?: number
}

export const initialTurnState: TurnState = {
  phase: 'idle',
  text: '',
  tools: [],
  citations: [],
  memories: [],
  warnings: [],
}

// reduce is the pure half of the hook, so tests can drive it without React.
export function reduce(state: TurnState, event: StreamEvent): TurnState {
  switch (event.type) {
    case 'message.start':
      return {
        ...initialTurnState,
        phase: 'streaming',
        runId: event.run_id,
        threadId: event.thread_id,
        startedAt: state.startedAt ?? Date.now(),
      }
    case 'message.delta':
      return { ...state, text: state.text + event.delta }
    case 'message.replace':
      // A replacement is a whole body, not a correction: everything this run
      // rendered is replaced rather than amended. The server only sends one
      // when the first generation tripped the citation check, so the deltas
      // that arrived before it were provisional text the user should never
      // keep.
      return { ...state, text: event.text }
    case 'tool.start':
      return {
        ...state,
        tools: [...state.tools, { call_id: event.call_id, tool: event.tool, status: 'running' }],
      }
    case 'tool.finish':
      return {
        ...state,
        tools: state.tools.map((tool) =>
          // Matched by id, not by index: two overlapping calls finish out of
          // order, and index pairing would attach one result to the wrong tool.
          tool.call_id === event.call_id
            ? { ...tool, status: event.status, latency_ms: event.latency_ms }
            : tool,
        ),
      }
    case 'citation': {
      const merged = new Set([...state.citations, ...event.evidence_ids])
      return { ...state, citations: [...merged] }
    }
    case 'state.awaiting_input':
      return {
        ...state,
        awaiting: {
          kind: 'clarification',
          state: event.state,
          pendingAction: event.pending_action,
          missingSlots: event.missing_slots,
        },
      }
    case 'confirmation.required':
      return {
        ...state,
        confirmation: {
          kind: 'confirmation',
          state: event.state,
          pendingAction: event.pending_action,
          summary: event.summary,
        },
      }
    case 'memory.saved':
      return {
        ...state,
        memories: [
          ...state.memories.filter((m) => m.memoryId !== event.memory_id),
          {
            memoryId: event.memory_id,
            memoryType: event.memory_type,
            content: event.content,
            refreshed: Boolean(event.refreshed),
          },
        ],
      }
    case 'message.end':
      return {
        ...state,
        // The awaiting states win over `done`: the thread is parked and the next
        // request is the user's, so reporting completion would be a different
        // claim than the one the server will answer with.
        phase: state.confirmation
          ? 'awaiting_confirm'
          : state.awaiting
            ? 'awaiting_user'
            : 'done',
        finishReason: event.finish_reason,
        usage: event.usage,
        warnings: event.warnings ?? state.warnings,
        elapsedMs: state.startedAt ? Date.now() - state.startedAt : undefined,
      }
    case 'error':
      return { ...state, phase: 'failed', error: event.message }
    default:
      // An unrecognised event must never break a turn. The union above is what
      // the server emits today, and it is what it will keep emitting — but the
      // next category of event arrives in a release, not in this file.
      return state
  }
}

export interface UseAgentTurn {
  state: TurnState
  send: (threadId: string, content: string) => void
  stop: () => void
  reset: () => void
  running: boolean
}

// useAgentTurn owns one in-flight turn.
//
// Deltas are batched rather than applied one at a time: a turn is thousands of
// them, and a render per delta would make the browser spend the whole turn
// re-rendering instead of appending text. 50ms is below the threshold where
// streaming reads as chunky and above where it reads as wasted work.
export function useAgentTurn(opts?: { onFinished?: (state: TurnState) => void }): UseAgentTurn {
  const [state, setState] = useState<TurnState>(initialTurnState)
  const handle = useRef<{ abort: () => void } | null>(null)
  const pending = useRef<StreamEvent[]>([])
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const onFinished = useRef(opts?.onFinished)
  onFinished.current = opts?.onFinished
  // notified remembers the last phase the callback fired for, so a re-render
  // within one phase does not report the same turn twice.
  const notified = useRef<TurnPhase>(initialTurnState.phase)

  const flush = useCallback(() => {
    timer.current = null
    const queued = pending.current
    pending.current = []
    if (queued.length === 0) return
    setState((current) => queued.reduce(reduce, current))
  }, [])

  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current)
      handle.current?.abort()
    },
    [],
  )

  const stop = useCallback(() => {
    handle.current?.abort()
    handle.current = null
    if (timer.current) {
      clearTimeout(timer.current)
      timer.current = null
    }
    setState((current) => ({ ...current, phase: 'canceled' }))
  }, [])

  const reset = useCallback(() => setState(initialTurnState), [])

  // Settled once per turn. The simplest thing that works is a phase watch: a
  // turn is over when the state leaves streaming, whichever way it left.
  useEffect(() => {
    if (state.phase === 'idle' || state.phase === 'streaming') {
      notified.current = state.phase
      return
    }
    if (notified.current === state.phase) return
    notified.current = state.phase
    onFinished.current?.(state)
  }, [state])

  const send = useCallback(
    (threadId: string, content: string) => {
      handle.current?.abort()
      if (timer.current) {
        clearTimeout(timer.current)
        timer.current = null
      }
      pending.current = []
      setState({ ...initialTurnState, phase: 'streaming', threadId, startedAt: Date.now() })

      const started = sendMessage({
        threadId,
        content,
        onEvent: (event) => {
          pending.current.push(event)
          if (!timer.current) timer.current = setTimeout(flush, 50)
        },
        onError: (message) => {
          pending.current = []
          setState((current) => ({ ...current, phase: 'failed', error: message }))
        },
      })
      handle.current = started

      void started.done.then(() => {
        // Flush whatever queue remains, then decide how the turn ended: a stream
        // that closed without message.end or error is an interrupted turn, and
        // saying so is the difference between "the model dropped out" and a page
        // that appears still to be working.
        if (timer.current) {
          clearTimeout(timer.current)
          timer.current = null
        }
        const queued = pending.current
        pending.current = []
        setState((current) => {
          const next = queued.reduce(reduce, current)
          if (next.phase !== 'streaming') return next
          return {
            ...next,
            phase: 'interrupted',
            elapsedMs: next.startedAt ? Date.now() - next.startedAt : undefined,
          }
        })
      })
    },
    [flush],
  )

  return {
    state,
    send,
    stop,
    reset,
    running: state.phase === 'streaming',
  }
}

// quoteState extracts the citation ids actually present in the answer text.
//
// A citation event is the set the server considered citable; the answer is what
// the model chose to mark up. Comparing them is how the console can say a
// footnote has nothing behind it instead of opening an empty drawer.
export function unresolvedCitations(text: string, citable: number[]): number[] {
  const available = new Set(citable)
  return collectCitationIds(text).filter((id) => !available.has(id))
}
