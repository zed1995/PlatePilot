import { render, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

import Ingestion from './Ingestion'

vi.mock('../api/client', () => ({
  adminApi: {
    batches: vi.fn(async () => ({
      items: [
        {
          batch_id: 25,
          stage: 'm2',
          status: 'success',
          started_at: '2025-01-01T00:00:00Z',
          duration_ms: 1200,
          rows_read: 100,
          accepted: 90,
          written: 90,
          rejected: 10,
        },
      ],
    })),
  },
}))

test('lists ingestion batches', async () => {
  render(
    <MemoryRouter>
      <Ingestion />
    </MemoryRouter>,
  )

  await waitFor(() => expect(document.body).toHaveTextContent('m2'))
  expect(document.body).toHaveTextContent('success')
})
