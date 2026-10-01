import { useEffect, useState } from 'react'
import { Alert, Table } from 'antd'
import type { ColumnsType } from 'antd/es/table'

// KeySetPage is the only shape the table needs from the API: the rows and the
// cursor of the following page.
export interface KeySetPage<T> {
  items: T[]
  next_cursor?: string
}

interface KeySetTableProps<T> {
  columns: ColumnsType<T>
  rowKey: (row: T) => string | number
  // fetchPage loads the page at cursor; undefined is the first page.
  fetchPage: (cursor?: string) => Promise<KeySetPage<T>>
  // resetKey identifies the current filter set. Changing it returns the
  // table to the first page, so a new search never continues an old one.
  resetKey?: string
  pageSize?: number
  rowClassName?: (row: T) => string
}

const defaultPageSize = 25

// KeySetTable is a paged table over opaque keyset cursors. It keeps the stack
// of cursors already visited, so "previous" needs no reverse query: the cursor
// that led to the current page is already known.
export default function KeySetTable<T>({
  columns,
  rowKey,
  fetchPage,
  resetKey = '',
  pageSize = defaultPageSize,
  rowClassName,
}: KeySetTableProps<T>) {
  // stack[i] is the cursor used to load page i; the first page is undefined.
  const [stack, setStack] = useState<(string | undefined)[]>([undefined])
  const [pageIndex, setPageIndex] = useState(0)
  const [items, setItems] = useState<T[]>([])
  const [nextCursor, setNextCursor] = useState<string | undefined>(undefined)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)

  const currentCursor = stack[pageIndex]

  // A new filter set restarts paging at the first page.
  useEffect(() => {
    setStack([undefined])
    setPageIndex(0)
  }, [resetKey, fetchPage])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    fetchPage(currentCursor)
      .then((page) => {
        if (cancelled) return
        setItems(page.items)
        setNextCursor(page.next_cursor)
      })
      .catch((cause) => {
        if (!cancelled) setError(cause)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
    // resetKey forces a refetch even when the cursor is already undefined.
  }, [fetchPage, currentCursor, resetKey])

  const goNext = () => {
    if (!nextCursor) return
    // Drop any forward history before pushing the new cursor.
    setStack((previous) => [...previous.slice(0, pageIndex + 1), nextCursor])
    setPageIndex((index) => index + 1)
  }

  const goBack = () => setPageIndex((index) => Math.max(0, index - 1))

  if (error) {
    return (
      <Alert
        type="error"
        showIcon
        message="Failed to load the page"
        description={error instanceof Error ? error.message : String(error)}
      />
    )
  }

  const hasNext = Boolean(nextCursor)

  return (
    <Table<T>
      size="small"
      columns={columns}
      dataSource={items}
      rowKey={rowKey}
      rowClassName={rowClassName}
      loading={loading}
      pagination={{
        current: pageIndex + 1,
        pageSize,
        // Inflate the total by one while another page exists: AntD then shows
        // the next arrow without needing the real row count.
        total: (pageIndex + 1) * pageSize + (hasNext ? 1 : 0),
        showSizeChanger: false,
        onChange: (page) => {
          if (page < pageIndex + 1) {
            goBack()
          } else if (page > pageIndex + 1) {
            goNext()
          }
        },
      }}
    />
  )
}
