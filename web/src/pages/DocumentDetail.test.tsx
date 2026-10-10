import { render, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

import DocumentDetailPage from './DocumentDetail'
import { adminApi } from '../api/client'

vi.mock('../api/client', () => ({
  adminApi: {
    document: vi.fn(),
    documentSourceReviews: vi.fn(),
  },
}))

function renderPage(id = 1) {
  const client = new QueryClient()
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/documents/${id}`]}>
        <Routes>
          <Route path="/documents/:id" element={<DocumentDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

test('renders meta + content and toggles vector preview without loading sources', async () => {
  vi.mocked(adminApi.document).mockImplementation(async (_id: number, withVector?: boolean) => ({
    document_id: 1,
    restaurant_id: 10,
    retrieval_scope: 'restaurant',
    doc_type: 'restaurant_profile',
    title: 'Joe profile',
    content_hash: 'profilehash123',
    version: 1,
    is_active: true,
    has_embedding: true,
    embedding_model: 'nomic',
    embedding_dimensions: 1024,
    content: 'hello world',
    metadata: { foo: 'bar' },
    source_record_ids: ['rec1'],
    vector_preview: withVector ? [0.1, 0.2] : undefined,
  }))

  renderPage()

  await waitFor(() => expect(document.body).toHaveTextContent('Joe profile'))
  expect(document.body).toHaveTextContent('hello world')
  // A non-digest document never triggers the source-reviews query.
  expect(adminApi.documentSourceReviews).not.toHaveBeenCalled()

  await userEvent.click(document.body.querySelector('button')!)
  await waitFor(() => expect(document.body).toHaveTextContent('Vector preview'))
})

test('digest shows the recorded source reviews linked to the document', async () => {
  vi.mocked(adminApi.document).mockResolvedValue({
    document_id: 3,
    restaurant_id: 10,
    retrieval_scope: 'restaurant',
    doc_type: 'restaurant_review_digest',
    title: 'Joe review digest',
    content_hash: 'digesthash123',
    version: 1,
    is_active: true,
    has_embedding: true,
    embedding_model: 'nomic',
    embedding_dimensions: 1024,
    content: 'digest body',
    metadata: {},
    source_record_ids: ['rec1'],
    source_review_ids: [101, 102],
  })
  vi.mocked(adminApi.documentSourceReviews).mockResolvedValue([
    {
      review_id: 101,
      restaurant_id: 10,
      rating: 5,
      reviewed_at: '2021-08-01T00:00:00Z',
      text: 'amazing broth and firm noodles',
      is_representative: true,
      topic_tags: ['food'],
    },
    {
      review_id: 102,
      restaurant_id: 10,
      rating: 4,
      reviewed_at: '2021-07-01T00:00:00Z',
      text: 'service was slow at noon',
      is_representative: true,
      topic_tags: ['service'],
    },
  ])

  renderPage(3)

  await waitFor(() => expect(document.body).toHaveTextContent('Source reviews (2)'))
  await waitFor(() => expect(document.body).toHaveTextContent('amazing broth and firm noodles'))
  expect(document.body).toHaveTextContent('service was slow at noon')
  expect(adminApi.documentSourceReviews).toHaveBeenCalledWith(3)
})
