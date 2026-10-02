import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Card, CardContent, CardHeader, CardTitle } from '../components/ui/card'
import { Button } from '../components/ui/button'
import { PageHeader } from '../components/page-header'
import { JsonBlock } from '../components/json-block'
import { ErrorState } from '../components/error-state'
import { adminApi } from '../api/client'

export function DocumentDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [vectorPreview, setVectorPreview] = useState(false)
  const detail = useQuery({
    queryKey: ['document', id, vectorPreview],
    queryFn: () => adminApi.document(Number(id), vectorPreview),
    enabled: Boolean(id),
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
          </CardContent>
        </Card>
        <Card className="lg:col-span-2">
          <CardHeader><CardTitle>Content</CardTitle></CardHeader>
          <CardContent><JsonBlock value={d?.content} /></CardContent>
        </Card>
      </div>
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

function Field({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex flex-col">
      <span className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">{label}</span>
      <span className="text-ink">{value ?? '-'}</span>
    </div>
  )
}

export default DocumentDetailPage