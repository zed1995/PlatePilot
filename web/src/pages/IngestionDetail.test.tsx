import { render, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

import IngestionDetailPage from './IngestionDetail'

vi.mock('../api/client', () => ({
  adminApi: {
    batch: vi.fn(async () => ({
      batch_id: 25,
      stage: 'm2',
      status: 'success',
      started_at: '2025-01-01T00:00:00Z',
      duration_ms: 1200,
      rows_read: 100,
      accepted: 100,
      written: 100,
      deduped: 0,
      filtered: 0,
      rejected: 0,
      unmatched: 0,
      curation_version: 'v1',
      missing_fields: {},
      rejections: [],
      rejections_truncated: false,
    })),
  },
}))

function renderPage() {
  const client = new QueryClient()
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/ingestion/25']}>
        <Routes>
          <Route path="/ingestion/:id" element={<IngestionDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

test('renders batch detail', async () => {
  renderPage()

  await waitFor(() => expect(document.body).toHaveTextContent('Batch 25'))
  await waitFor(() => expect(document.body).toHaveTextContent('success'))
})