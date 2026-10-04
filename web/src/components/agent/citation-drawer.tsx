// citation-drawer.tsx is what makes a footnote checkable.
//
// A [^n] in the answer is a claim with a pointer. Until the pointer opens onto
// the document it names, the number is decoration. So this fetches the one
// document on demand rather than prefetching the whole citation set: a turn
// cites six to ten documents and the user reads one.
import { useQuery } from '@tanstack/react-query'

import { chatApi } from '../../api/chat'
import type { Evidence, EvidenceTrace } from '../../api/types'
import { formatTime } from '../../format'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { StatusTag } from '../status-tag'
import { ErrorState } from '../error-state'

export interface CitationDrawerProps {
  evidenceId: number | null
  onClose: () => void
}

export function citationKey(evidenceId: number | null) {
  return ['evidence', evidenceId ?? 0] as const
}

export function CitationDrawer({ evidenceId, onClose }: CitationDrawerProps) {
  const open = evidenceId !== null
  const query = useQuery({
    queryKey: citationKey(evidenceId),
    queryFn: () => chatApi.fetchEvidence({ evidence_ids: [evidenceId as number] }),
    enabled: open,
    select: (bundle) => ({
      evidence: bundle.evidence.find((item) => item.evidence_id === evidenceId),
      trace: bundle.trace,
    }),
  })

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent
        data-testid="citation-drawer"
        className="left-auto right-4 top-4 h-[calc(100vh-32px)] w-[min(560px,calc(100vw-32px))] max-w-none translate-x-0 translate-y-0 overflow-auto"
      >
        <DialogHeader>
          <DialogTitle>引用 #{evidenceId ?? '-'}</DialogTitle>
          <DialogDescription>
            这一条就是回答里那条脚注所依据的原文，数据时间以快照为准。
          </DialogDescription>
        </DialogHeader>

        {query.isLoading && <p className="m-0 text-[12px] text-ink-tertiary">读取证据…</p>}

        {query.error ? (
          <ErrorState
            title="证据读取失败"
            description={query.error instanceof Error ? query.error.message : String(query.error)}
            onRetry={() => void query.refetch()}
          />
        ) : null}

        {open && !query.isLoading && !query.error && !query.data?.evidence && (
          // The id came from the answer text, but the server no longer offers it
          // as citable. Saying so is the whole point of checking: a drawer that
          // opened blank would look the same as one that opened correctly.
          <div className="rounded-lg border border-error/20 bg-error/5 p-3 text-[12px] text-error">
            引用不可用：服务端没有返回 id 为 {evidenceId} 的可引用文档。它可能已下线，或者这条脚注
            越出了本轮证据集。
          </div>
        )}

        {query.data?.trace && <EvidenceTraceStrip trace={query.data.trace} />}
        {query.data?.evidence && <CitationBody evidence={query.data.evidence} />}
      </DialogContent>
    </Dialog>
  )
}

function CitationBody({ evidence }: { evidence: Evidence }) {
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[13px] font-medium text-ink">
          {evidence.restaurant_name ?? `餐厅 #${evidence.restaurant_id}`}
        </span>
        <StatusTag tone="grey">{evidence.doc_type}</StatusTag>
        {evidence.topic && <StatusTag tone="blue">{evidence.topic}</StatusTag>}
      </div>

      <dl className="grid grid-cols-2 gap-2 text-[11px]">
        <Field label="restaurant_id">{evidence.restaurant_id}</Field>
        <Field label="evidence_id">{evidence.evidence_id}</Field>
        <Field label="source">{evidence.source}</Field>
        <Field label="snapshot_at">{formatTime(evidence.snapshot_at)}</Field>
        {evidence.score !== undefined && <Field label="score">{evidence.score}</Field>}
        {evidence.title && <Field label="title">{evidence.title}</Field>}
      </dl>

      <div>
        <p className="m-0 pb-1 text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">
          原文
        </p>
        <div className="whitespace-pre-wrap rounded-lg border border-[var(--border-subtle)] bg-black/[0.02] p-3 text-[12px] leading-[1.7] text-ink">
          {evidence.content}
        </div>
      </div>

      {evidence.source_record_ids && evidence.source_record_ids.length > 0 && (
        <p className="m-0 text-[11px] text-ink-tertiary">
          来源记录：{evidence.source_record_ids.join(', ')}
        </p>
      )}
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col">
      <dt className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">{label}</dt>
      <dd className="m-0 break-all text-ink">{children}</dd>
    </div>
  )
}

// EvidenceTraceStrip is the recall's own account of what it did.
//
// The numbers are not decoration: `dropped` with a reason is how a citation that
// should have existed turns out to have been assembled away, and a warning about
// the vector channel degrading is the only place that degradation is visible for
// a recall that happened outside a conversation.
function EvidenceTraceStrip({ trace }: { trace: EvidenceTrace }) {
  const warnings = trace.warnings ?? []
  return (
    <div className="space-y-1.5">
      <p className="m-0 text-[11px] text-ink-tertiary tabular">
        recall: scope={trace.scope_size} · recalled={trace.recalled} · kept={trace.kept} ·
        dropped={trace.dropped} · tokens={trace.tokens}/{trace.token_budget}
        {trace.embedding_model_id ? ` · ${trace.embedding_model_id}` : ''}
      </p>
      {trace.dropped_by_reason && Object.keys(trace.dropped_by_reason).length > 0 && (
        <p className="m-0 text-[11px] text-ink-tertiary">
          丢弃原因：
          {Object.entries(trace.dropped_by_reason)
            .map(([reason, count]) => `${reason}=${count}`)
            .join(' · ')}
        </p>
      )}
      {warnings.length > 0 && (
        <ul className="m-0 list-disc pl-4 text-[11px] text-warning">
          {warnings.map((warning) => (
            <li key={warning}>{warning}</li>
          ))}
        </ul>
      )}
    </div>
  )
}
