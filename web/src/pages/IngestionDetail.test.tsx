import { render, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

import IngestionDetailPage from './IngestionDetail'

vi.mock('../api/client', () => ({
  adminApi: {
    batch: vi.fn(async () => ({
      batch_id: 25,
      stage: 'm2',
      status: 'failed',
      started_at: '2025-01-01T00:00:00Z',
      finished_at: '2025-01-01T00:00:01Z',
      duration_ms: 1200,
      curation_version: '2025-01',
      rows_read: 100,
      accepted: 80,
      written: 80,
      deduped: 0,
      filtered: 0,
      rejected: 20,
      unmatched: 0,
      missing_fields: { address: 3 },
      reject_reasons: { 'missing borough': 12, 'bad rating': 8 },
      rejections: [
        { stage: 'm2', line_no: 42, reason: 'missing borough' },
      ],
      rejections_truncated: true,
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

test('renders counts, reject distribution, and truncation warning', async () => {
  renderPage()

  await waitFor(() => expect(document.body).toHaveTextContent('missing borough'))
  expect(document.body).toHaveTextContent('bad rating')
  expect(document.body).toHaveTextContent('truncated')
})
