import { useQuery } from '@tanstack/react-query'
import { Link, useParams } from 'react-router-dom'
import { Card, CardContent, CardHeader, CardTitle } from '../components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../components/ui/table'
import { PageHeader } from '../components/page-header'
import { StatusTag } from '../components/status-tag'
import { ErrorState } from '../components/error-state'
import { JsonBlock } from '../components/json-block'
import { adminApi } from '../api/client'
import { formatTime } from '../format'

export function RestaurantDetailPage() {
  const { id } = useParams<{ id: string }>()
  const detail = useQuery({ queryKey: ['restaurant', id], queryFn: () => adminApi.restaurant(Number(id)), enabled: Boolean(id) })
  const docs = useQuery({ queryKey: ['restaurant', id, 'documents'], queryFn: () => adminApi.restaurantDocuments(Number(id)), enabled: Boolean(id) })

  if (detail.isError) return <ErrorState title="Failed to load restaurant" description={detail.error instanceof Error ? detail.error.message : String(detail.error)} onRetry={() => detail.refetch()} />

  const r = detail.data
  const active = (r as unknown as { is_active_for_demo?: boolean } | undefined)?.is_active_for_demo
  return (
    <div className="space-y-5">
      <PageHeader
        title={r?.name ?? 'Restaurant'}
        description={r?.address ?? ''}
        extra={<StatusTag tone={active ? 'green' : 'grey'}>{active ? 'active' : 'inactive'}</StatusTag>}
      />
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader><CardTitle>Overview</CardTitle></CardHeader>
          <CardContent className="space-y-2 text-[13px]">
            <div className="grid grid-cols-2 gap-3 md:grid-cols-3">
              <Field label="Restaurant ID" value={r?.restaurant_id} />
              <Field label="Borough" value={r?.borough ?? '-'} />
              <Field label="Price level" value={r?.price_level ?? '-'} />
              <Field label="Cuisines" value={r?.cuisines?.join(' · ') ?? '-'} />
              <Field label="Rating" value={r?.rating_computed_avg ?? '-'} />
              <Field label="Reviews" value={r?.rating_count} />
              <Field label="Observed" value={r ? formatTime(r.observed_at) : '-'} />
              <Field label="Knowledge score" value={r?.knowledge_score} />
              <Field label="Snapshot" value={r?.snapshot_status ?? '-'} />
            </div>
            {r?.description && <p className="mt-3 text-[13px] text-ink-secondary">{r.description}</p>}
          </CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle>Stats</CardTitle></CardHeader>
          <CardContent className="space-y-2 text-[13px]">
            <Field label="Source reviews" value={r?.source_review_count} />
            <Field label="Stored reviews" value={r?.stored_review_count} />
            <Field label="Text reviews" value={r?.text_review_count} />
            <Field label="Embedded reviews" value={r?.embedded_review_count} />
            <Field label="Last reviewed" value={r?.last_reviewed_at ? formatTime(r.last_reviewed_at) : '-'} />
          </CardContent>
        </Card>
      </div>

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <Card>
          <CardHeader><CardTitle>Attributes</CardTitle></CardHeader>
          <CardContent><JsonBlock value={r?.attributes} /></CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle>Hours</CardTitle></CardHeader>
          <CardContent><JsonBlock value={r?.hours} /></CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader><CardTitle>Recent documents</CardTitle></CardHeader>
        <CardContent>
          {(docs.data ?? []).length === 0 ? <p className="text-[13px] text-ink-tertiary">No documents for this restaurant.</p> : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>ID</TableHead>
                  <TableHead>Scope</TableHead>
                  <TableHead>Type</TableHead>
                  <TableHead>Active</TableHead>
                  <TableHead>Has embedding</TableHead>
                  <TableHead>Version</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(docs.data ?? []).map((d) => (
                  <TableRow key={d.document_id}>
                    <TableCell><Link className="text-ink hover:underline" to={`/documents/${d.document_id}`}>{d.document_id}</Link></TableCell>
                    <TableCell>{d.retrieval_scope}</TableCell>
                    <TableCell>{d.doc_type}</TableCell>
                    <TableCell>{d.is_active ? 'yes' : 'no'}</TableCell>
                    <TableCell>{d.has_embedding ? 'yes' : 'no'}</TableCell>
                    <TableCell className="tabular">{d.version}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
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

export default RestaurantDetailPage