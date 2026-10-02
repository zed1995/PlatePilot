import { useCallback, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Card, CardContent } from '../components/ui/card'
import { Input } from '../components/ui/input'
import { Button } from '../components/ui/button'
import { PageHeader } from '../components/page-header'
import { KeySetTable, type KeySetPage } from '../components/key-set-table'
import { adminApi, type DocumentsParams } from '../api/client'
import type { DocumentListItem } from '../api/types'

interface Filters { restaurantId?: string; scope?: string; docType?: string; isActive?: boolean }

export function Documents() {
  const [filters, setFilters] = useState<Filters>({})
  const fetchPage = useCallback((cursor?: string) => {
    const p: DocumentsParams = {
      cursor,
      limit: 25,
      restaurant_id: filters.restaurantId ? Number(filters.restaurantId) : undefined,
      scope: filters.scope || undefined,
      doc_type: filters.docType || undefined,
      is_active: filters.isActive,
    }
    return adminApi.documents(p)
  }, [filters])

  const columns = useMemo(() => ([
    { key: 'id', width: 80, header: 'ID', cell: (d: DocumentListItem) => <Link className="text-ink hover:underline" to={`/documents/${d.document_id}`}>{d.document_id}</Link> },
    { key: 'restaurant', width: 110, header: 'Restaurant', cell: (d: DocumentListItem) => <Link className="text-ink hover:underline" to={`/restaurants/${d.restaurant_id}`}>{d.restaurant_id}</Link> },
    { key: 'scope', width: 130, header: 'Scope', cell: (d: DocumentListItem) => d.retrieval_scope },
    { key: 'type', header: 'Doc type', cell: (d: DocumentListItem) => d.doc_type },
    { key: 'title', header: 'Title', cell: (d: DocumentListItem) => d.title ?? '-' },
    { key: 'active', width: 80, header: 'Active', cell: (d: DocumentListItem) => d.is_active ? 'yes' : 'no' },
    { key: 'embed', width: 120, header: 'Has embedding', cell: (d: DocumentListItem) => d.has_embedding ? 'yes' : 'no' },
    { key: 'version', width: 80, header: 'Version', cell: (d: DocumentListItem) => <span className="tabular">{d.version}</span> },
  ]), [])

  return (
    <div className="space-y-5">
      <PageHeader title="Documents" description="Indexed documents with retrieval scope and embedding state." />
      <Card>
        <CardContent className="pt-5">
          <div className="flex flex-wrap items-end gap-3">
            <div className="flex flex-col gap-1">
              <label className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">Restaurant ID</label>
              <Input className="w-[150px]" value={filters.restaurantId ?? ''} onChange={(e) => setFilters((f) => ({ ...f, restaurantId: e.target.value || undefined }))} />
            </div>
            <div className="flex flex-col gap-1">
              <label className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">Scope</label>
              <Input className="w-[150px]" value={filters.scope ?? ''} onChange={(e) => setFilters((f) => ({ ...f, scope: e.target.value || undefined }))} />
            </div>
            <div className="flex flex-col gap-1">
              <label className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">Doc type</label>
              <Input className="w-[180px]" value={filters.docType ?? ''} onChange={(e) => setFilters((f) => ({ ...f, docType: e.target.value || undefined }))} />
            </div>
            <label className="ml-2 inline-flex items-center gap-2 text-[13px] text-ink">
              <input type="checkbox" checked={Boolean(filters.isActive)} onChange={(e) => setFilters((f) => ({ ...f, isActive: e.target.checked || undefined }))} />
              Active only
            </label>
            <Button variant="ghost" size="sm" onClick={() => setFilters({})}>Reset</Button>
          </div>
        </CardContent>
      </Card>
      <KeySetTable<DocumentListItem>
        columns={columns as unknown as { key: string; width?: number; header: React.ReactNode; cell: (row: DocumentListItem) => React.ReactNode }[]}
        rowKey={(d) => d.document_id}
        fetchPage={fetchPage as unknown as (cursor?: string) => Promise<KeySetPage<DocumentListItem>>}
        resetKey={JSON.stringify(filters)}
      />
    </div>
  )
}

export default Documents