import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

import RestaurantDetailPage from './RestaurantDetail'

vi.mock('../api/client', () => ({
  adminApi: {
    restaurant: vi.fn(async () => ({
      restaurant_id: 123,
      name: 'Joe',
      source: 'google_local',
      cuisines: ['pizza'],
      rating_count: 10,
      stored_review_count: 10,
      text_review_count: 9,
      representative_review_count: 3,
      embedded_review_count: 3,
      is_active_for_demo: true,
      snapshot_status: 'ready',
      attributes: { outdoor_seating: true },
      hours: { Monday: '10-22' },
    })),
    summaries: vi.fn(async () => []),
    restaurantDocuments: vi.fn(async () => [
      {
        document_id: 9,
        restaurant_id: 123,
        retrieval_scope: 'restaurant',
        doc_type: 'restaurant_profile',
        content_hash: 'abc123',
        version: 1,
        is_active: true,
        has_embedding: true,
      },
    ]),
    reviews: vi.fn(async () => ({ items: [] })),
  },
}))

function renderPage() {
  const client = new QueryClient()
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/restaurants/123']}>
        <Routes>
          <Route path="/restaurants/:id" element={<RestaurantDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

test('renders basic information', async () => {
  renderPage()

  await waitFor(() => expect(document.body).toHaveTextContent('Joe'))
  expect(document.body).toHaveTextContent('pizza')
  // The detail payload carries the same demo flag as the list row, so the
  // header badge must render "active", not fall back to "inactive".
  expect(screen.getByText('active')).toBeInTheDocument()
})