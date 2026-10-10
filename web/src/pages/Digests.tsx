import { useCallback, useState } from 'react'
import { Link } from 'react-router-dom'
import { Card, CardContent } from '../components/ui/card'
import { Input } from '../components/ui/input'
import { PageHeader } from '../components/page-header'
import { KeySetTable, type KeySetPage } from '../components/key-set-table'
import { adminApi, type DocumentsParams } from '../api/client'
import type { DocumentListItem } from '../api/types'

// restaurant_review_digest: one model-backed comprehension document per
// restaurant. The filter is fixed to that doc type and restaurant scope.
const digestDocType = 'restaurant_review_digest'

export function Digests() {
  const [restaurantId, setRestaurantId] = useState('')
  const [activeOnly, setActiveOnly] = useState(false)

  const fetchPage = useCallback((cursor?: string) => {
    const params: DocumentsParams = {
      cursor,
      limit: 25,
      scope: 'restaurant',
      doc_type: digestDocType,
      restaurant_id: restaurantId ? Number(restaurantId) : undefined,
      is_active: activeOnly || undefined,
    }
    return adminApi.documents(params)
  }, [restaurantId, activeOnly])

  const columns = [
    { key: 'id', width: 80, header: 'ID', cell: (d: DocumentListItem) => <Link className="text-ink hover:underline" to={`/documents/${d.document_id}`}>{d.document_id}</Link> },
    { key: 'restaurant', width: 110, header: 'Restaurant', cell: (d: DocumentListItem) => <Link className="text-ink hover:underline" to={`/restaurants/${d.restaurant_id}`}>{d.restaurant_id}</Link> },
    { key: 'title', header: 'Title', cell: (d: DocumentListItem) => d.title ?? '-' },
    { key: 'sources', width: 90, header: 'Sources', cell: (d: DocumentListItem) => <Link className="text-ink hover:underline" to={`/documents/${d.document_id}`}>view</Link> },
    { key: 'active', width: 80, header: 'Active', cell: (d:DocumentListItem) => d.is_active ? 'yes' : 'no' },
    { key: 'embed', width: 120, header: 'Has embedding', cell: (d: DocumentListItem) => d.has_embedding ? 'yes' : 'no' },
    { key: 'version', width: 80, header: 'Version', cell: (d: DocumentListItem) => <span className="tabular">{d.version}</span> },
  ]

  return (
    <div className="space-y-5">
      <PageHeader
        title="Review Digests"
        description="One model-backed review comprehension per restaurant, with the source reviews it was grounded in."
      />
      <Card>
        <CardContent className="pt-5">
          <div className="flex flex-wrap items-end gap-3">
            <div className="flex flex-col gap-1">
              <label className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">Restaurant ID</label>
              <Input
                className="w-[150px]"
                value={restaurantId}
                onChange={(e) => setRestaurantId(e.target.value)}
              />
            </div>
            <label className="inline-flex items-center gap-2 text-[13px] text-ink">
              <input
                type="checkbox"
                checked={activeOnly}
                onChange={(e) => setActiveOnly(e.target.checked)}
              />
              Active only
            </label>
          </div>
        </CardContent>
      </Card>
      <KeySetTable<DocumentListItem>
        columns={columns as unknown as { key: string; width?: number; header: React.ReactNode; cell: (row: DocumentListItem) => React.ReactNode }[]}
        rowKey={(d) => d.document_id}
        fetchPage={fetchPage as unknown as (cursor?: string) => Promise<KeySetPage<DocumentListItem>>}
        resetKey={`${restaurantId}:${activeOnly}`}
      />
    </div>
  )
}

export default Digests
