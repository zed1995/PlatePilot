import { useCallback, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Card, CardContent } from '../components/ui/card'
import { Input } from '../components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../components/ui/select'
import { Button } from '../components/ui/button'
import { PageHeader } from '../components/page-header'
import { KeySetTable, type KeySetPage } from '../components/key-set-table'
import { StatusTag } from '../components/status-tag'
import { adminApi, type RestaurantsParams } from '../api/client'
import type { RestaurantListItem } from '../api/types'
import { formatTime } from '../format'

interface Filters { borough?: string; cuisine?: string; active?: boolean; q?: string }

const boroughOptions = [
  { value: 'manhattan', label: 'Manhattan' },
  { value: 'brooklyn', label: 'Brooklyn' },
  { value: 'queens', label: 'Queens' },
  { value: 'bronx', label: 'Bronx' },
  { value: 'staten island', label: 'Staten Island' },
]

export function Restaurants() {
  const [filters, setFilters] = useState<Filters>({})

  const fetchPage = useCallback(
    (cursor?: string) => {
      const p: RestaurantsParams = {
        cursor,
        limit: 25,
        borough: filters.borough || undefined,
        cuisine: filters.cuisine || undefined,
        active: filters.active,
        q: filters.q || undefined,
      }
      return adminApi.restaurants(p)
    },
    [filters],
  )

  const columns = useMemo(() => ([
    { key: 'id', width: 80, header: 'ID', cell: (r: RestaurantListItem) => <Link className="text-ink hover:underline" to={`/restaurants/${r.restaurant_id}`}>{r.restaurant_id}</Link> },
    { key: 'name', header: 'Name', cell: (r: RestaurantListItem) => (
      <div className="flex flex-col">
        <Link className="text-ink hover:underline" to={`/restaurants/${r.restaurant_id}`}>{r.name}</Link>
        <span className="text-[12px] text-ink-tertiary">{r.address ?? ''}</span>
      </div>
    ) },
    { key: 'borough', width: 110, header: 'Borough', cell: (r: RestaurantListItem) => r.borough ?? '-' },
    { key: 'cuisines', header: 'Cuisines', cell: (r: RestaurantListItem) => r.cuisines?.length ? r.cuisines.join(' · ') : '-' },
    { key: 'price', width: 80, header: 'Price', cell: (r: RestaurantListItem) => r.price_level ?? '-' },
    { key: 'rating', width: 120, header: 'Rating', cell: (r: RestaurantListItem) => (
      <div className="flex flex-col"><span className="tabular">{r.rating_computed_avg ?? '-'}</span><span className="text-[12px] text-ink-tertiary">{r.rating_count} reviews</span></div>
    ) },
    { key: 'active', width: 90, header: 'Active', cell: (r: RestaurantListItem) => <StatusTag tone={r.is_active_for_demo ? 'green' : 'grey'}>{r.is_active_for_demo ? 'active' : 'inactive'}</StatusTag> },
    { key: 'observed', width: 170, header: 'Observed', cell: (r: RestaurantListItem) => formatTime(r.observed_at) },
  ]), [])

  return (
    <div className="space-y-5">
      <PageHeader
        title="Restaurants"
        description="Directory of restaurants with demo status, cuisines, and ratings."
      />
      <Card>
        <CardContent className="pt-5">
          <div className="flex flex-wrap items-end gap-3">
            <div className="flex flex-col gap-1">
              <label className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">Borough</label>
              <Select value={filters.borough ?? ''} onValueChange={(v: string) => setFilters((f) => ({ ...f, borough: v || undefined }))}>
                <SelectTrigger className="w-[150px]"><SelectValue placeholder="any" /></SelectTrigger>
                <SelectContent>{boroughOptions.map((o) => <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>)}</SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <label className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">Cuisine</label>
              <Input className="w-[150px]" placeholder="pizza" value={filters.cuisine ?? ''} onChange={(e) => setFilters((f) => ({ ...f, cuisine: e.target.value || undefined }))} />
            </div>
            <div className="flex flex-col gap-1">
              <label className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">Name contains</label>
              <Input className="w-[160px]" placeholder="Joe" value={filters.q ?? ''} onChange={(e) => setFilters((f) => ({ ...f, q: e.target.value || undefined }))} />
            </div>
            <label className="ml-2 inline-flex items-center gap-2 text-[13px] text-ink">
              <input type="checkbox" checked={Boolean(filters.active)} onChange={(e) => setFilters((f) => ({ ...f, active: e.target.checked || undefined }))} />
              Active only
            </label>
            <Button variant="ghost" size="sm" onClick={() => setFilters({})}>Reset</Button>
          </div>
        </CardContent>
      </Card>

      <KeySetTable<RestaurantListItem>
        columns={columns as unknown as { key: string; width?: number; header: React.ReactNode; cell: (row: RestaurantListItem) => React.ReactNode }[]}
        rowKey={(r) => r.restaurant_id}
        fetchPage={fetchPage as unknown as (cursor?: string) => Promise<KeySetPage<RestaurantListItem>>}
        resetKey={JSON.stringify(filters)}
      />
    </div>
  )
}

export default Restaurants