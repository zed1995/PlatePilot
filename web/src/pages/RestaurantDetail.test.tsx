import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
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
      snapshot_status: 'ready',
      attributes: { outdoor_seating: true },
      hours: { Monday: '10-22' },
    })),
    summaries: vi.fn(async () => [
      {
        restaurant_id: 123,
        topic: 'service',
        sentiment: 0.4,
        positive_ratio: 0.7,
        summary: 'friendly staff',
        evidence_count: 5,
        generated_at: '2021-09-01T00:00:00Z',
      },
    ]),
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
    reviews: vi.fn(async () => ({
      items: [
        {
          review_id: 55,
          restaurant_id: 123,
          rating: 5,
          reviewed_at: '2019-05-01T00:00:00Z',
          text: 'great pizza',
          is_representative: true,
          topic_tags: ['food'],
        },
      ],
    })),
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

test('renders basic information and switches to the reviews tab', async () => {
  renderPage()

  await waitFor(() => expect(document.body).toHaveTextContent('Joe'))
  expect(document.body).toHaveTextContent('pizza')

  await userEvent.click(
    await screen.findByRole('tab', { name: /Reviews/ }),
  )

  await waitFor(() => expect(document.body).toHaveTextContent('great pizza'))
})
