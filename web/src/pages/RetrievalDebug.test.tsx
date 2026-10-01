import { render, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'

import RetrievalDebug from './RetrievalDebug'
import type { SearchResponse } from '../api/types'

const searchResponse: SearchResponse = {
  candidates: [
    {
      restaurant_id: 7,
      name: "Joe's Pizza",
      address: 'Carmine St',
      score: 0.92,
      reasons: ['name match'],
    },
  ],
  trace: {
    candidate_pool: 1,
    returned: 1,
    top_k: 5,
    rerank_applied: false,
    channels: [{ channel: 'keyword', ran: true, weight: 0.5, results: 1 }],
  },
}

vi.mock('../api/client', () => ({
  adminApi: {
    debugSearch: vi.fn(async () => searchResponse),
    debugEvidence: vi.fn(async () => ({
      evidence: [
        {
          evidence_id: 1,
          restaurant_id: 7,
          doc_type: 'restaurant_profile',
          content: 'known for pizza',
          source: 'google_local',
          snapshot_at: '2021-09-01T00:00:00Z',
        },
      ],
    })),
  },
}))

function renderPage() {
  return render(
    <MemoryRouter>
      <RetrievalDebug />
    </MemoryRouter>,
  )
}

test('runs a retrieval and renders the candidate and its trace', async () => {
  renderPage()

  const queryInput = document.querySelector<HTMLInputElement>(
    'input#query',
  )!
  await userEvent.type(queryInput, 'best pizza')
  await userEvent.click(document.querySelector('button[type="submit"]')!)

  await waitFor(() => expect(document.body).toHaveTextContent("Joe's Pizza"))
  expect(document.body).toHaveTextContent('0.92')
  // The trace panel surfaces the channel activity.
  expect(document.body).toHaveTextContent('keyword')
})

test('loads evidence for a candidate', async () => {
  renderPage()

  await userEvent.click(document.querySelector('button[type="submit"]')!)
  await waitFor(() => expect(document.body).toHaveTextContent("Joe's Pizza"))

  await userEvent.click(
    document.querySelector<HTMLButtonElement>('button.ant-btn-sm')!,
  )

  await waitFor(() => expect(document.body).toHaveTextContent('known for pizza'))
})
