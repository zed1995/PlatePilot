import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'

import RetrievalDebug from './RetrievalDebug'

vi.mock('../api/client', () => ({
  adminApi: {
    debugSearch: vi.fn(async () => ({
      candidates: [
        { restaurant_id: 1, name: 'Joe Pizza', score: 0.9, rating: 4.5, address: '123 Carmine St' },
      ],
      trace: { candidate_pool: 5, returned: 1, top_k: 5, rerank_applied: false },
    })),
    debugEvidence: vi.fn(async () => ({ evidence: [] })),
  },
}))

test('runs search and shows candidates', async () => {
  const user = userEvent.setup()
  render(
    <MemoryRouter>
      <RetrievalDebug />
    </MemoryRouter>,
  )

  await user.click(screen.getByRole('button', { name: /run retrieval/i }))

  await waitFor(() => expect(document.body).toHaveTextContent('Joe Pizza'))
})