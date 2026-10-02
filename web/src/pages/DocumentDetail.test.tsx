import { render, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

import DocumentDetailPage from './DocumentDetail'

vi.mock('../api/client', () => ({
  adminApi: {
    document: vi.fn(async (_id: number, withVector: boolean) => ({
      document_id: 1,
      restaurant_id: 10,
      retrieval_scope: 'restaurant',
      doc_type: 'restaurant_profile',
      title: 'Joe profile',
      version: 1,
      is_active: true,
      has_embedding: true,
      embedding_model: 'nomic',
      embedding_dimensions: 1024,
      content: 'hello world',
      metadata: { foo: 'bar' },
      source_record_ids: ['rec1'],
      vector_preview: withVector ? [0.1, 0.2] : undefined,
    })),
  },
}))

function renderPage() {
  const client = new QueryClient()
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/documents/1']}>
        <Routes>
          <Route path="/documents/:id" element={<DocumentDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

test('renders meta + content and toggles vector preview', async () => {
  renderPage()

  await waitFor(() => expect(document.body).toHaveTextContent('Joe profile'))
  expect(document.body).toHaveTextContent('hello world')

  await userEvent.click(document.body.querySelector('button')!)
  await waitFor(() => expect(document.body).toHaveTextContent('Vector preview'))
})