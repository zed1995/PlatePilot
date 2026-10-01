import { render, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

import Dashboard from './Dashboard'

vi.mock('../api/client', () => ({
  adminApi: {
    overview: vi.fn(async () => ({
      tables: {
        restaurants_total: 36133,
        restaurants_active: 3000,
        reviews_estimate: 8_727_344,
        reviews_estimated: true,
        documents_active: 11775,
        batches_total: 25,
      },
      document_breakdown: [
        { retrieval_scope: 'restaurant', doc_type: 'restaurant_profile', count: 3000 },
      ],
      active_documents_without_vector: 0,
      recent_batches: [
        {
          batch_id: 25,
          stage: 'm2',
          status: 'success',
          started_at: '2025-01-01T00:00:00Z',
          duration_ms: 1200,
        },
      ],
      migrations: [{ version: '000003', applied_at: '2025-01-01T00:00:00Z' }],
      environment: { embedding_model: 'nomic', embedding_dimensions: 1024 },
    })),
    boundaries: vi.fn(async () => [
      {
        boundary_id: 1,
        name: 'Manhattan',
        kind: 'borough',
        loaded_at: '2025-01-01T00:00:00Z',
      },
    ]),
  },
}))

function renderDashboard() {
  const client = new QueryClient()
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

test('renders the table cards and healthy vector status', async () => {
  renderDashboard()

  await waitFor(() => expect(document.body).toHaveTextContent('36,133'))
  expect(document.body).toHaveTextContent('3000')
  expect(document.body).toHaveTextContent('every active document carries a vector')
  expect(document.body).toHaveTextContent('Manhattan')
})
