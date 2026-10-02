import { useCallback, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Card, CardContent } from '../components/ui/card'
import { Input } from '../components/ui/input'
import { Button } from '../components/ui/button'
import { PageHeader } from '../components/page-header'
import { KeySetTable, type KeySetPage } from '../components/key-set-table'
import { StatusTag } from '../components/status-tag'
import { adminApi, type BatchesParams } from '../api/client'
import type { BatchListItem } from '../api/types'
import { formatDuration, formatTime, statusColor } from '../format'

export function Ingestion() {
  const [stage, setStage] = useState('')
  const fetchPage = useCallback((cursor?: string) => {
    const p: BatchesParams = { cursor, limit: 25, stage: stage || undefined }
    return adminApi.batches(p)
  }, [stage])

  const columns = useMemo(() => ([
    { key: 'id', width: 80, header: 'ID', cell: (b: BatchListItem) => <Link className="text-ink hover:underline" to={`/ingestion/${b.batch_id}`}>{b.batch_id}</Link> },
    { key: 'stage', width: 90, header: 'Stage', cell: (b: BatchListItem) => b.stage },
    { key: 'status', width: 110, header: 'Status', cell: (b: BatchListItem) => <StatusTag tone={statusColor(b.status)}>{b.status}</StatusTag> },
    { key: 'started', header: 'Started', cell: (b: BatchListItem) => formatTime(b.started_at) },
    { key: 'duration', width: 100, header: 'Duration', cell: (b: BatchListItem) => <span className="tabular">{formatDuration(b.duration_ms)}</span> },
    { key: 'written', width: 90, header: 'Written', cell: (b: BatchListItem) => <span className="tabular">{b.written}</span> },
    { key: 'rejected', width: 90, header: 'Rejected', cell: (b: BatchListItem) => <span className={b.rejected > 0 ? 'text-error tabular' : 'tabular'}>{b.rejected}</span> },
  ]), [])

  return (
    <div className="space-y-5">
      <PageHeader title="Ingestion" description="Ingestion batches with stage, status, and counts." />
      <Card>
        <CardContent className="pt-5">
          <div className="flex flex-wrap items-end gap-3">
            <div className="flex flex-col gap-1">
              <label className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">Stage</label>
              <Input className="w-[180px]" placeholder="curation / ingest / embed" value={stage} onChange={(e) => setStage(e.target.value)} />
            </div>
            <Button variant="ghost" size="sm" onClick={() => setStage('')}>Reset</Button>
          </div>
        </CardContent>
      </Card>
      <KeySetTable<BatchListItem>
        columns={columns as unknown as { key: string; width?: number; header: React.ReactNode; cell: (row: BatchListItem) => React.ReactNode }[]}
        rowKey={(b) => b.batch_id}
        fetchPage={fetchPage as unknown as (cursor?: string) => Promise<KeySetPage<BatchListItem>>}
        resetKey={stage}
      />
    </div>
  )
}

export default Ingestion