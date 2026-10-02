import { useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Card, CardContent, CardHeader, CardTitle } from '../components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../components/ui/table'
import { PageHeader } from '../components/page-header'
import { StatusTag } from '../components/status-tag'
import { JsonBlock } from '../components/json-block'
import { ErrorState } from '../components/error-state'
import { adminApi } from '../api/client'
import type { BatchDetail } from '../api/types'
import { formatDuration, formatTime, statusColor } from '../format'

export function IngestionDetailPage() {
  const { id } = useParams<{ id: string }>()
  const detail = useQuery<BatchDetail>({ queryKey: ['batch', id], queryFn: () => adminApi.batch(Number(id)), enabled: Boolean(id) })

  if (detail.isError) return <ErrorState title="Failed to load batch" description={detail.error instanceof Error ? detail.error.message : String(detail.error)} onRetry={() => detail.refetch()} />

  const b = detail.data
  return (
    <div className="space-y-5">
      <PageHeader
        title={`Batch ${id}`}
        description={b ? `${formatTime(b.started_at)} · ${formatDuration(b.duration_ms)}` : ''}
        extra={b ? <StatusTag tone={statusColor(b.status)}>{b.status}</StatusTag> : null}
      />
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        <Card>
          <CardHeader><CardTitle>Counts</CardTitle></CardHeader>
          <CardContent className="space-y-2 text-[13px]">
            <Field label="Rows read" value={b?.rows_read} />
            <Field label="Accepted" value={b?.accepted} />
            <Field label="Written" value={b?.written} />
            <Field label="Deduped" value={b?.deduped} />
            <Field label="Filtered" value={b?.filtered} />
            <Field label="Rejected" value={b?.rejected} />
            <Field label="Unmatched" value={b?.unmatched} />
          </CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle>Embedding</CardTitle></CardHeader>
          <CardContent className="space-y-2 text-[13px]">
            <Field label="Documents built" value={b?.documents_built} />
            <Field label="Documents embedded" value={b?.documents_embedded} />
            <Field label="Documents rejected" value={b?.documents_rejected} />
            <Field label="Model" value={b?.embedding_model ?? '-'} />
            <Field label="Dimensions" value={b?.embedding_dimensions ?? '-'} />
          </CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle>Provenance</CardTitle></CardHeader>
          <CardContent className="space-y-2 text-[13px]">
            <Field label="Curation version" value={b?.curation_version} />
            <Field label="Source file" value={b?.source_file ?? '-'} />
            <Field label="Boundary version" value={b?.boundary_version ?? '-'} />
            <Field label="Error code" value={b?.error_code ?? '-'} />
          </CardContent>
        </Card>
      </div>

      {b && (b.reject_reasons || b.missing_fields !== undefined) && (
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
          {b.reject_reasons ? (
            <Card>
              <CardHeader><CardTitle>Reject reasons</CardTitle></CardHeader>
              <CardContent><JsonBlock value={b.reject_reasons} /></CardContent>
            </Card>
          ) : null}
          {b.missing_fields !== undefined ? (
            <Card>
              <CardHeader><CardTitle>Missing fields</CardTitle></CardHeader>
              <CardContent><JsonBlock value={b.missing_fields} /></CardContent>
            </Card>
          ) : null}
        </div>
      )}

      {b?.rejections && b.rejections.length > 0 && (
        <Card>
          <CardHeader><CardTitle>Rejections{b.rejections_truncated ? ' (truncated)' : ''}</CardTitle></CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow><TableHead>Stage</TableHead><TableHead>Line</TableHead><TableHead>Reason</TableHead><TableHead>Source record</TableHead></TableRow>
              </TableHeader>
              <TableBody>
                {b.rejections.map((r, i) => (
                  <TableRow key={`${r.stage}-${r.line_no}-${i}`}>
                    <TableCell>{r.stage}</TableCell>
                    <TableCell className="tabular">{r.line_no}</TableCell>
                    <TableCell>{r.reason}</TableCell>
                    <TableCell>{r.source_record_id ?? '-'}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}
    </div>
  )
}

function Field({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex flex-col">
      <span className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">{label}</span>
      <span className="text-ink">{value === undefined || value === null || value === '' ? '-' : value}</span>
    </div>
  )
}

export default IngestionDetailPage