import { render, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

import DocumentDetailPage from './DocumentDetail'

vi.mock('../api/client', () => ({
  adminApi: {
    document: vi.fn(async (_id: number, vectorPreview: boolean) => ({
      document_id: 77,
      restaurant_id: 123,
      retrieval_scope: 'restaurant',
      doc_type: 'restaurant_profile',
      title: 'Joe profile',
      content: 'Joe serves classic slices.',
      content_hash: 'hash77',
      version: 1,
      is_active: true,
      has_embedding: true,
      metadata: { source: 'google_local' },
      source_record_ids: ['rec-1'],
      vector_preview: vectorPreview ? [0.1, 0.2] : undefined,
    })),
  },
}))

function renderPage() {
  const client = new QueryClient()
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/documents/77']}>
        <Routes>
          <Route path="/documents/:id" element={<DocumentDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

test('renders the content and loads the vector preview on demand', async () => {
  const { adminApi } = await import('../api/client')
  renderPage()

  await waitFor(() => expect(document.body).toHaveTextContent('Joe profile'))
  expect(document.body).toHaveTextContent('Joe serves classic slices.')

  await userEvent.click(
    document.querySelector<HTMLButtonElement>('#vector-preview-switch')!,
  )

  await waitFor(() =>
    expect(adminApi.document).toHaveBeenLastCalledWith(77, true),
  )
})
