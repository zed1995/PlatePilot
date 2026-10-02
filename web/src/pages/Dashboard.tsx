import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Card, CardContent, CardHeader, CardTitle } from '../components/ui/card'
import { Table, TableCell, TableHead, TableHeader, TableRow, TableBody } from '../components/ui/table'
import { PageHeader } from '../components/page-header'
import { StatCard } from '../components/stat-card'
import { StatusTag } from '../components/status-tag'
import { ErrorState } from '../components/error-state'
import { EmptyState } from '../components/empty-state'
import { adminApi } from '../api/client'
import type { BatchListItem, DocumentCount, Migration, Overview, Boundary } from '../api/types'
import { formatDuration, formatTime, statusColor } from '../format'

export function Dashboard() {
  const overview = useQuery<Overview>({ queryKey: ['overview'], queryFn: adminApi.overview })
  const boundaries = useQuery<Boundary[]>({ queryKey: ['boundaries'], queryFn: adminApi.boundaries })

  if (overview.isError) return <ErrorState title="Failed to load the overview" description={overview.error instanceof Error ? overview.error.message : String(overview.error)} onRetry={() => overview.refetch()} />

  const tables = overview.data?.tables
  return (
    <div className="space-y-5">
      <PageHeader title="Overview" description="Health of the knowledge base and recent ingestion activity." />
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard label="Restaurants (active)" value={tables?.restaurants_total ?? 0} suffix={`/ ${tables?.restaurants_active ?? 0}`} loading={overview.isLoading} />
        <StatCard label={tables?.reviews_estimated ? 'Reviews (estimated)' : 'Reviews'} value={tables?.reviews_estimate ?? 0} loading={overview.isLoading} />
        <StatCard label="Active documents" value={tables?.documents_active ?? 0} loading={overview.isLoading} />
        <StatCard label="Batches" value={tables?.batches_total ?? 0} loading={overview.isLoading} />
      </div>

      {overview.data && (overview.data.active_documents_without_vector === 0
        ? <p className="text-[12px] text-ink-tertiary">Vector health: every active document carries a vector.</p>
        : <div className="flex items-center gap-2 rounded-lg border border-error/20 bg-error/5 p-3 text-[13px] text-error">
            <span>Vector health violation: {overview.data.active_documents_without_vector} active documents lack a vector.</span>
          </div>)}

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        <Card>
          <CardHeader><CardTitle>Document distribution (active)</CardTitle></CardHeader>
          <CardContent>
            {(overview.data?.document_breakdown ?? []).length === 0 ? <EmptyState title="No documents" /> : (
              <Table>
                <TableHeader><TableRow><TableHead>Scope</TableHead><TableHead>Doc type</TableHead><TableHead className="text-right">Count</TableHead></TableRow></TableHeader>
                <TableBody>
                  {(overview.data?.document_breakdown ?? []).map((d: DocumentCount) => (
                    <TableRow key={`${d.retrieval_scope}-${d.doc_type}`}>
                      <TableCell>{d.retrieval_scope}</TableCell>
                      <TableCell className="truncate">{d.doc_type}</TableCell>
                      <TableCell className="tabular text-right">{d.count}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>

        <Card className="lg:col-span-2">
          <CardHeader><CardTitle>Recent batches</CardTitle></CardHeader>
          <CardContent>
            {(overview.data?.recent_batches ?? []).length === 0 ? <EmptyState title="No recent batches" /> : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-[80px]">Batch</TableHead>
                    <TableHead>Stage</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead>Started</TableHead>
                    <TableHead className="text-right">Duration</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {(overview.data?.recent_batches ?? []).map((b: BatchListItem) => (
                    <TableRow key={b.batch_id}>
                      <TableCell><Link className="text-ink hover:underline" to={`/ingestion/${b.batch_id}`}>{b.batch_id}</Link></TableCell>
                      <TableCell>{b.stage}</TableCell>
                      <TableCell><StatusTag tone={statusColor(b.status)}>{b.status}</StatusTag></TableCell>
                      <TableCell>{formatTime(b.started_at)}</TableCell>
                      <TableCell className="tabular text-right">{formatDuration(b.duration_ms)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
      </div>

      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
        <Card>
          <CardHeader><CardTitle>Environment</CardTitle></CardHeader>
          <CardContent className="space-y-2 text-[13px]">
            <div><div className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">Embedding model</div><div className="text-ink">{overview.data?.environment.embedding_model || '-'}</div></div>
            <div><div className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">Dimensions</div><div className="tabular text-ink">{overview.data?.environment.embedding_dimensions || '-'}</div></div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle>Migrations</CardTitle></CardHeader>
          <CardContent>
            {(overview.data?.migrations ?? []).length === 0 ? <EmptyState title="No migrations" /> : (
              <Table>
                <TableHeader><TableRow><TableHead>Version</TableHead><TableHead>Applied</TableHead></TableRow></TableHeader>
                <TableBody>
                  {(overview.data?.migrations ?? []).map((m: Migration) => (
                    <TableRow key={m.version}>
                      <TableCell>{m.version}</TableCell>
                      <TableCell>{formatTime(m.applied_at)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle>Boundaries</CardTitle></CardHeader>
          <CardContent>
            {(boundaries.data ?? []).length === 0 ? <EmptyState title="No boundaries" /> : (
              <Table>
                <TableHeader><TableRow><TableHead>ID</TableHead><TableHead>Name</TableHead><TableHead>Kind</TableHead><TableHead>Loaded</TableHead></TableRow></TableHeader>
                <TableBody>
                  {(boundaries.data ?? []).map((b: Boundary) => (
                    <TableRow key={b.boundary_id}>
                      <TableCell className="tabular">{b.boundary_id}</TableCell>
                      <TableCell className="truncate">{b.name}</TableCell>
                      <TableCell>{b.kind}</TableCell>
                      <TableCell>{formatTime(b.loaded_at)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  )
}

export default Dashboard