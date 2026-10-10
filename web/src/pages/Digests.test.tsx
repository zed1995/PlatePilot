import { render, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

import Digests from './Digests'
import { adminApi } from '../api/client'

vi.mock('../api/client', () => ({
  adminApi: {
    documents: vi.fn(async () => ({
      items: [
        {
          document_id: 5,
          restaurant_id: 10,
          retrieval_scope: 'restaurant',
          doc_type: 'restaurant_review_digest',
          title: 'Joe review digest',
          version: 1,
          is_active: true,
          has_embedding: true,
        },
      ],
    })),
  },
}))

test('lists review digests with the fixed digest filter', async () => {
  render(
    <MemoryRouter>
      <Digests />
    </MemoryRouter>,
  )

  await waitFor(() => expect(document.body).toHaveTextContent('Joe review digest'))
  expect(adminApi.documents).toHaveBeenCalledWith(expect.objectContaining({
    scope: 'restaurant',
    doc_type: 'restaurant_review_digest',
  }))
})
