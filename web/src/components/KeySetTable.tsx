import { useEffect, useRef, useState } from 'react'
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
  // "fixed" keeps wide unbreakable cells from pushing the table past the
  // container edge; pair it with ellipsis on the wide columns.
  tableLayout?: 'auto' | 'fixed'
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
  tableLayout,
}: KeySetTableProps<T>) {
  // stack[i] is the cursor used to load page i; the first page is undefined.
  const [stack, setStack] = useState<(string | undefined)[]>([undefined])
  const [pageIndex, setPageIndex] = useState(0)
  const [items, setItems] = useState<T[]>([])
  const [nextCursor, setNextCursor] = useState<string | undefined>(undefined)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  // Bumped when the filter set changes so the fetch below runs exactly once
  // with the fresh first-page cursor, never once more with a stale one.
  const [generation, setGeneration] = useState(0)
  const firstRun = useRef(true)

  const currentCursor = stack[pageIndex]

  // A new filter set restarts paging at the first page.
  useEffect(() => {
    if (firstRun.current) {
      firstRun.current = false
      return
    }
    setStack([undefined])
    setPageIndex(0)
    setGeneration((gen) => gen + 1)
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
  }, [fetchPage, currentCursor, generation])

  const goNext = () => {
    if (!nextCursor) return
    // Drop any forward history before pushing the new cursor.
    setStack((previous) => [...previous.slice(0, pageIndex + 1), nextCursor])
    setPageIndex((index) => index + 1)
  }

  // Clicking any earlier page number moves through the visited cursor stack,
  // and the next button (the only reachable forward step) appends a cursor.
  const goTo = (page: number) => {
    const target = page - 1
    if (target === pageIndex) return
    if (target > pageIndex) {
      goNext()
    } else {
      setPageIndex(target)
    }
  }

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
      size="middle"
      tableLayout={tableLayout}
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
        onChange: goTo,
      }}
    />
  )
}
