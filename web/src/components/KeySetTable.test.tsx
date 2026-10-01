import { render, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ColumnsType } from 'antd/es/table'

import KeySetTable, { type KeySetPage } from './KeySetTable'

interface Row {
  id: number
  name: string
}

const columns: ColumnsType<Row> = [
  { title: 'ID', dataIndex: 'id' },
  { title: 'Name', dataIndex: 'name' },
]

// makeFetch serves two pages: ids 1,2 on page one (with a cursor), id 3 on
// page two (last page).
function makeFetch() {
  const pages: Record<string, KeySetPage<Row>> = {
    '': {
      items: [
        { id: 1, name: 'alpha' },
        { id: 2, name: 'bravo' },
      ],
      next_cursor: 'cursor-2',
    },
    'cursor-2': {
      items: [{ id: 3, name: 'charlie' }],
    },
  }
  const calls: (string | undefined)[] = []
  const fetchPage = vi.fn((cursor?: string) => {
    calls.push(cursor)
    return Promise.resolve(pages[cursor ?? ''])
  })
  return { fetchPage, calls }
}

test('walks to the next page and back without duplicates', async () => {
  const { fetchPage, calls } = makeFetch()
  render(
    <KeySetTable<Row>
      columns={columns}
      rowKey={(row) => row.id}
      fetchPage={fetchPage}
      pageSize={2}
    />,
  )

  await waitFor(() => expect(document.body).toHaveTextContent('alpha'))
  expect(document.body).toHaveTextContent('bravo')

  const nextButton = document.querySelector(
    '.ant-pagination-next:not(.ant-pagination-disabled)',
  )!
  await userEvent.click(nextButton)

  await waitFor(() => expect(document.body).toHaveTextContent('charlie'))
  expect(document.body).not.toHaveTextContent('alpha')
  expect(calls[calls.length - 1]).toBe('cursor-2')

  const prevButton = document.querySelector(
    '.ant-pagination-prev:not(.ant-pagination-disabled)',
  )!
  await userEvent.click(prevButton)

  await waitFor(() => expect(document.body).toHaveTextContent('alpha'))
  expect(document.body).toHaveTextContent('bravo')
  // The previous page uses its original cursor, not a fresh first page call.
  expect(calls[calls.length - 1]).toBeUndefined()
})

test('resets to the first page when resetKey changes', async () => {
  const { fetchPage } = makeFetch()
  const { rerender } = render(
    <KeySetTable<Row>
      columns={columns}
      rowKey={(row) => row.id}
      fetchPage={fetchPage}
      pageSize={2}
      resetKey="filter-set-1"
    />,
  )

  await waitFor(() => expect(document.body).toHaveTextContent('alpha'))
  await userEvent.click(
    document.querySelector('.ant-pagination-next:not(.ant-pagination-disabled)')!,
  )
  await waitFor(() => expect(document.body).toHaveTextContent('charlie'))

  rerender(
    <KeySetTable<Row>
      columns={columns}
      rowKey={(row) => row.id}
      fetchPage={fetchPage}
      pageSize={2}
      resetKey="filter-set-2"
    />,
  )

  await waitFor(() => expect(document.body).toHaveTextContent('alpha'))
})

test('surfaces a fetch failure', async () => {
  const failing = vi.fn(() => Promise.reject(new Error('boom')))
  render(
    <KeySetTable<Row>
      columns={columns}
      rowKey={(row) => row.id}
      fetchPage={failing}
    />,
  )
  await waitFor(() => expect(document.body).toHaveTextContent('boom'))
})
