import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Card, CardContent, CardHeader, CardTitle } from '../components/ui/card'
import { Button } from '../components/ui/button'
import { PageHeader } from '../components/page-header'
import { JsonBlock } from '../components/json-block'
import { ErrorState } from '../components/error-state'
import { adminApi } from '../api/client'
import type { ReviewListItem } from '../api/types'
import { formatTime } from '../format'

const digestDocType = 'restaurant_review_digest'

export function DocumentDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [vectorPreview, setVectorPreview] = useState(false)
  const detail = useQuery({
    queryKey: ['document', id, vectorPreview],
    queryFn: () => adminApi.document(Number(id), vectorPreview),
    enabled: Boolean(id),
  })

  const isDigest = detail.data?.doc_type === digestDocType
  const sourceReviews = useQuery({
    queryKey: ['document-source-reviews', id],
    queryFn: () => adminApi.documentSourceReviews(Number(id)),
    enabled: Boolean(id) && isDigest,
  })

  if (detail.isError) return <ErrorState title="Failed to load document" description={detail.error instanceof Error ? detail.error.message : String(detail.error)} onRetry={() => detail.refetch()} />

  const d = detail.data
  return (
    <div className="space-y-5">
      <PageHeader
        title={d?.title ?? `Document ${id}`}
        description={`${d?.doc_type ?? ''} · ${d?.retrieval_scope ?? ''}`}
        extra={<Button variant="outline" size="sm" onClick={() => setVectorPreview((v) => !v)}>{vectorPreview ? 'Hide vector preview' : 'Show vector preview'}</Button>}
      />
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        <Card>
          <CardHeader><CardTitle>Meta</CardTitle></CardHeader>
          <CardContent className="space-y-2 text-[13px]">
            <Field label="Document ID" value={d?.document_id} />
            <Field label="Restaurant" value={<Link className="text-ink hover:underline" to={`/restaurants/${d?.restaurant_id}`}>{d?.restaurant_id}</Link>} />
            <Field label="Active" value={d?.is_active ? 'yes' : 'no'} />
            <Field label="Has embedding" value={d?.has_embedding ? 'yes' : 'no'} />
            <Field label="Embedding model" value={d?.embedding_model ?? '-'} />
            <Field label="Dimensions" value={d?.embedding_dimensions ?? '-'} />
            <Field label="Version" value={d?.version} />
            {isDigest && (
              <Field label="Source reviews" value={d?.source_review_ids?.length ?? 0} />
            )}
          </CardContent>
        </Card>
        <Card className="lg:col-span-2">
          <CardHeader><CardTitle>Content</CardTitle></CardHeader>
          <CardContent><JsonBlock value={d?.content} /></CardContent>
        </Card>
      </div>
      {isDigest && (
        <Card>
          <CardHeader>
            <CardTitle>Source reviews ({d?.source_review_ids?.length ?? 0})</CardTitle>
          </CardHeader>
          <CardContent>
            <SourceReviewsBlock
              loading={sourceReviews.isLoading}
              error={sourceReviews.error}
              items={sourceReviews.data ?? []}
              recordedCount={d?.source_review_ids?.length ?? 0}
              onRetry={() => sourceReviews.refetch()}
            />
          </CardContent>
        </Card>
      )}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <Card>
          <CardHeader><CardTitle>Metadata</CardTitle></CardHeader>
          <CardContent><JsonBlock value={d?.metadata} /></CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle>Source records</CardTitle></CardHeader>
          <CardContent><JsonBlock value={d?.source_record_ids} /></CardContent>
        </Card>
      </div>
      {vectorPreview && (
        <Card>
          <CardHeader><CardTitle>Vector preview</CardTitle></CardHeader>
          <CardContent><JsonBlock value={d?.vector_preview} /></CardContent>
        </Card>
      )}
    </div>
  )
}

function SourceReviewsBlock({
  loading, error, items, recordedCount, onRetry,
}: {
  loading: boolean
  error: unknown
  items: ReviewListItem[]
  recordedCount: number
  onRetry: () => void
}) {
  if (loading) {
    return <p className="m-0 text-[13px] text-ink-secondary">Loading source reviews…</p>
  }
  if (error) {
    return (
      <div className="space-y-2">
        <p className="m-0 text-[13px] text-red-600">
          Failed to load source reviews: {error instanceof Error ? error.message : String(error)}
        </p>
        <Button variant="outline" size="sm" onClick={onRetry}>Retry</Button>
      </div>
    )
  }
  if (recordedCount === 0) {
    return (
      <p className="m-0 text-[13px] text-ink-secondary">
        This digest recorded no source review ids. Re-run build-digests to regenerate it with provenance.
      </p>
    )
  }
  if (items.length === 0) {
    return (
      <p className="m-0 text-[13px] text-ink-secondary">
        No source reviews could be resolved; the recorded review ids may no longer exist.
      </p>
    )
  }
  return (
    <ul className="m-0 space-y-3 p-0">
      {items.map((review) => (
        <li key={review.review_id} className="rounded-lg border border-[var(--border-subtle)] p-3">
          <div className="mb-1 flex items-center gap-3 text-[11px] uppercase tracking-[0.04em] text-ink-tertiary">
            <span className="tabular">#{review.review_id}</span>
            <span>{review.rating} stars</span>
            <span>{formatTime(review.reviewed_at)}</span>
          </div>
          <p className="m-0 whitespace-pre-wrap text-[13px] leading-relaxed text-ink">
            {review.text || '(review without text)'}
          </p>
        </li>
      ))}
    </ul>
  )
}

function Field({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex flex-col">
      <span className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">{label}</span>
      <span className="text-ink">{value ?? '-'}</span>
    </div>
  )
}

export default DocumentDetailPage