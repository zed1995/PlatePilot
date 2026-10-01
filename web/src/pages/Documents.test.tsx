import { render, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

import Documents from './Documents'

vi.mock('../api/client', () => ({
  adminApi: {
    documents: vi.fn(async () => ({
      items: [
        {
          document_id: 1,
          restaurant_id: 10,
          retrieval_scope: 'restaurant',
          doc_type: 'restaurant_profile',
          content_hash: 'activehash123',
          version: 1,
          is_active: true,
          has_embedding: true,
        },
        {
          document_id: 2,
          restaurant_id: 10,
          retrieval_scope: 'restaurant',
          doc_type: 'restaurant_profile',
          content_hash: 'oldhash456',
          version: 0,
          is_active: false,
          has_embedding: false,
        },
      ],
    })),
  },
}))

test('lists documents and grays out inactive versions', async () => {
  render(
    <MemoryRouter>
      <Documents />
    </MemoryRouter>,
  )

  await waitFor(() => expect(document.body).toHaveTextContent('restaurant_profile'))
  expect(document.querySelector('.inactive-row')).not.toBeNull()
  // The default-no-filter notice is always shown.
  expect(document.body).toHaveTextContent('is_active is not filtered by default')
})
