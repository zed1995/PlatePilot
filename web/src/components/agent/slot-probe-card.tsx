// slot-probe-card.tsx is the fastest way to find out what the agent heard.
//
// It answers one question — did the layer that turns a sentence into filters
// understand this? — without spending a model turn, building a thread or writing
// anything. `interpret` is a pure projection, so this card is safe to poke at
// repeatedly, which is exactly what makes it the first screen to open when an
// answer comes back wrong.
//
// The hard/soft split is the entire point. "安静" is a soft condition the
// reranker reads; "4 星以上" is a hard filter the structured channel enforces.
// Rendering both in one list would hide the invariant this card exists to show,
// so they get separate sections and separate visual weight.
import { useState } from 'react'
import { Loader2, Send } from 'lucide-react'

import { chatApi } from '../../api/chat'
import type { InterpretResult, RestaurantFilter } from '../../api/types'
import { JsonBlock } from '../json-block'
import { StatusTag } from '../status-tag'
import { Button } from '../ui/button'
import { Textarea } from '../ui/textarea'

// hardFields names the keys that are hard filters, in the order a person reads
// them. Anything the plan grows that is not listed here still shows up in the
// raw block below — this list decides emphasis, never completeness.
const hardFields: { key: keyof RestaurantFilter; label: string }[] = [
  { key: 'borough', label: 'borough' },
  { key: 'neighborhood', label: 'neighborhood' },
  { key: 'cuisines', label: 'cuisines' },
  { key: 'price_levels', label: 'price_levels' },
  { key: 'min_rating', label: 'min_rating' },
  { key: 'open_now', label: 'open_now' },
]

function fieldValue(value: unknown): string {
  if (Array.isArray(value)) return value.join(', ')
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  return String(value)
}

export interface SlotProbeCardProps {
  onSendToAgent?: (text: string) => void
  initialText?: string
}

export function SlotProbeCard({ onSendToAgent, initialText = '' }: SlotProbeCardProps) {
  const [text, setText] = useState(initialText)
  const [result, setResult] = useState<InterpretResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const trimmed = text.trim()

  async function probe() {
    if (!trimmed || busy) return
    setBusy(true)
    setError(null)
    try {
      setResult(await chatApi.interpret(trimmed))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-2.5" data-testid="slot-probe-card">
      <p className="m-0 text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">槽位探针</p>

      <Textarea
        value={text}
        aria-label="探针输入"
        onChange={(event) => setText(event.target.value)}
        placeholder="曼哈顿中城 4 星以上意大利菜，要安静适合约会"
        className="min-h-[56px]"
      />
      <div className="flex items-center gap-2">
        <Button size="sm" onClick={() => void probe()} disabled={!trimmed || busy}>
          {busy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          解析这句
        </Button>
        {onSendToAgent && (
          <Button
            size="sm"
            variant="outline"
            disabled={!trimmed}
            onClick={() => onSendToAgent(trimmed)}
          >
            <Send className="h-3.5 w-3.5" />
            发给 Agent
          </Button>
        )}
      </div>

      {error && <p className="m-0 text-[11px] text-error">解析失败：{error}</p>}

      {result && <ProbeResult result={result} />}
    </div>
  )
}

export function ProbeResult({ result }: { result: InterpretResult }) {
  const hard = hardFields
    .map(({ key, label }) => ({ label, value: result.hard_filters?.[key] }))
    .filter(({ value }) => value !== undefined && value !== null && value !== '')

  return (
    <div className="space-y-2.5" data-testid="probe-result">
      <div className="flex flex-wrap items-center gap-2">
        <StatusTag tone="blue">{result.intent}</StatusTag>
        {/* source=rules means the slots came from the rule fallback, not a model.
            Without it a mechanical-looking split is indistinguishable from a
            worse model. */}
        <StatusTag tone={result.source === 'model' ? 'green' : 'orange'}>
          source={result.source}
        </StatusTag>
        {result.extract_latency_ms !== undefined && (
          <span className="text-[11px] text-ink-tertiary tabular">
            {result.extract_latency_ms}ms
          </span>
        )}
        {result.need_clarification && <StatusTag tone="orange">需要澄清</StatusTag>}
      </div>

      {/* Hard filters: enforced by the structured channel, so they are rendered
          as fixed key/value chips with no soft-condition styling anywhere
          near them. */}
      <section className="rounded-lg border border-[var(--border-subtle)] bg-surface-solid px-2.5 py-2">
        <p className="m-0 text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">
          硬条件 hard_filters
        </p>
        {hard.length === 0 ? (
          <p className="m-0 mt-1 text-[12px] text-ink-tertiary">没有解析出硬条件。</p>
        ) : (
          <ul className="m-0 mt-1.5 list-none space-y-1 p-0">
            {hard.map(({ label, value }) => (
              <li key={label} className="flex items-baseline gap-2 text-[12px]">
                <code className="shrink-0 font-mono text-[11px] text-ink-tertiary">{label}</code>
                <span className="text-ink">{fieldValue(value)}</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      {/* Soft conditions: prose the reranker scores. Visually separated on
          purpose — one list for both is how "安静" ends up treated as a filter. */}
      <section className="rounded-lg border border-dashed border-[var(--border-default)] bg-black/[0.02] px-2.5 py-2">
        <p className="m-0 text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">
          软条件 soft_conditions
        </p>
        {result.soft_conditions.length === 0 ? (
          <p className="m-0 mt-1 text-[12px] text-ink-tertiary">没有解析出软条件。</p>
        ) : (
          <ul className="m-0 mt-1.5 list-none space-y-1 p-0">
            {result.soft_conditions.map((condition, index) => (
              <li key={`${condition.topic}-${index}`} className="flex items-baseline gap-2 text-[12px]">
                <StatusTag tone="grey">{condition.topic}</StatusTag>
                <span className="text-ink">{condition.text}</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      {result.named_restaurants.length > 0 && (
        <p className="m-0 text-[11px] text-ink-secondary">
          提到店名：{result.named_restaurants.join('、')}
        </p>
      )}

      {result.missing_slots.length > 0 && (
        <p className="m-0 text-[11px] text-warning">缺槽位：{result.missing_slots.join('、')}</p>
      )}

      {result.warnings && result.warnings.length > 0 && (
        <ul className="m-0 list-disc pl-4 text-[11px] text-warning">
          {result.warnings.map((warning) => (
            <li key={warning}>{warning}</li>
          ))}
        </ul>
      )}

      {/* The structured reading above is a convenience layer over this object.
          When the plan grows a field, the raw block is what shows it — which is
          why it is always rendered rather than shown only on a miss. */}
      <details>
        <summary className="cursor-pointer text-[11px] text-ink-tertiary">
          hard_filters 原始 JSON
        </summary>
        <div className="pt-1.5">
          <JsonBlock value={result.hard_filters} />
        </div>
      </details>
    </div>
  )
}
