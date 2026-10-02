import { useEffect, useRef, useState } from 'react'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './ui/table'
import { Button } from './ui/button'
import { ErrorState } from './error-state'

export interface KeySetPage<T> { items: T[]; next_cursor?: string }
interface KeySetTableProps<T> {
  columns: { key: string; width?: number; header: React.ReactNode; cell: (row: T) => React.ReactNode }[]
  rowKey: (row: T) => string | number
  fetchPage: (cursor?: string) => Promise<KeySetPage<T>>
  resetKey?: string
  pageSize?: number
  rowClassName?: (row: T) => string
  tableLayout?: 'auto' | 'fixed'
}

export function KeySetTable<T>({
  columns, rowKey, fetchPage, resetKey = '', rowClassName, tableLayout = 'auto',
}: KeySetTableProps<T>) {
  const [stack, setStack] = useState<(string | undefined)[]>([undefined])
  const [pageIndex, setPageIndex] = useState(0)
  const [items, setItems] = useState<T[]>([])
  const [nextCursor, setNextCursor] = useState<string | undefined>(undefined)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [generation, setGeneration] = useState(0)
  const firstRun = useRef(true)
  const currentCursor = stack[pageIndex]

  useEffect(() => {
    if (firstRun.current) { firstRun.current = false; return }
    setStack([undefined]); setPageIndex(0); setGeneration((g) => g + 1)
  }, [resetKey, fetchPage])

  useEffect(() => {
    let cancelled = false
    setLoading(true); setError(null)
    fetchPage(currentCursor)
      .then((page) => { if (cancelled) return; setItems(page.items); setNextCursor(page.next_cursor) })
      .catch((cause) => { if (!cancelled) setError(cause) })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [fetchPage, currentCursor, generation])

  if (error) return <ErrorState title="Failed to load the page" description={error instanceof Error ? error.message : String(error)} onRetry={() => setGeneration((g) => g + 1)} />

  const hasNext = Boolean(nextCursor)
  return (
    <div className="space-y-3">
      <div className={loading ? 'opacity-60 pointer-events-none' : undefined}>
        <Table style={{ tableLayout }}>
          <TableHeader>
            <TableRow>
              {columns.map((c) => (
                <TableHead key={c.key} style={{ width: c.width }}>{c.header}</TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((row) => (
              <TableRow key={rowKey(row)} data-class-name={rowClassName?.(row)}>
                {columns.map((c) => (
                  <TableCell key={c.key}>{c.cell(row)}</TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      <div className="flex items-center justify-between text-[12px] text-ink-tertiary">
        <span>Page {pageIndex + 1}{hasNext ? '+' : ''}</span>
        <div className="flex gap-2">
          <Button variant="ghost" size="sm" disabled={pageIndex === 0} onClick={() => setPageIndex((i) => Math.max(0, i - 1))}>Previous</Button>
          <Button variant="outline" size="sm" disabled={!hasNext} onClick={() => {
            if (!nextCursor) return
            setStack((p) => [...p.slice(0, pageIndex + 1), nextCursor])
            setPageIndex((i) => i + 1)
          }}>Next</Button>
        </div>
      </div>
    </div>
  )
}