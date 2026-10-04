import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import type { Evidence } from '../../api/types'
import { CitationDrawer } from './citation-drawer'

const fetchEvidence = vi.hoisted(() => vi.fn())

vi.mock('../../api/chat', () => ({
  chatApi: { fetchEvidence },
  getUserId: () => 'demo-user',
  setUserId: vi.fn(),
}))

const document: Evidence = {
  evidence_id: 7,
  restaurant_id: 42,
  restaurant_name: 'Tabetomo',
  doc_type: 'restaurant_review_summary',
  title: '安静度',
  content: '多位食客提到店内安静，适合聊天。',
  source: 'google_places',
  snapshot_at: '2026-09-30T12:00:00Z',
  topic: 'quiet',
  score: 0.81,
}

function renderDrawer(evidenceId: number | null, onClose = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <CitationDrawer evidenceId={evidenceId} onClose={onClose} />
    </QueryClientProvider>,
  )
  return onClose
}

beforeEach(() => {
  fetchEvidence.mockReset()
  fetchEvidence.mockResolvedValue({ evidence: [document] })
})

// The drawer is opened by a footnote in the answer, so the id it asks for is the
// id in the text — not a restaurant, not an index into the citation set. Getting
// this wrong is how a footnote ends up showing the wrong review.
test('asking for one citation fetches exactly that document id', async () => {
  renderDrawer(7)

  expect(fetchEvidence).toHaveBeenCalledTimes(1)
  expect(fetchEvidence).toHaveBeenCalledWith({ evidence_ids: [7] })

  expect(await screen.findByText('Tabetomo')).toBeInTheDocument()
  expect(screen.getByText('restaurant_review_summary')).toBeInTheDocument()
  expect(screen.getByText('多位食客提到店内安静，适合聊天。')).toBeInTheDocument()
  expect(screen.getByText('google_places')).toBeInTheDocument()
  expect(screen.getByText('2026-09-30 12:00:00Z')).toBeInTheDocument()
})

// The recall's own numbers travel with the document. Dropping them would leave a
// citation that looks checked while the reason it was assembled the way it was
// has been thrown away.
test('the recall trace and its warnings come along', async () => {
  fetchEvidence.mockResolvedValue({
    evidence: [document],
    trace: {
      scope_size: 1,
      top_k: 3,
      recalled: 1,
      kept: 1,
      dropped: 0,
      tokens: 120,
      token_budget: 4000,
      embedding_model_id: 'qwen3-embedding:0.6b',
      warnings: ['证据退化成按文档顺序排序'],
    },
  })
  renderDrawer(7)

  expect(await screen.findByText(/kept=1/)).toBeInTheDocument()
  expect(screen.getByText(/qwen3-embedding:0.6b/)).toBeInTheDocument()
  expect(screen.getByText('证据退化成按文档顺序排序')).toBeInTheDocument()
})

test('a closed drawer asks for nothing', () => {
  renderDrawer(null)
  expect(fetchEvidence).not.toHaveBeenCalled()
})

// An id the server no longer offers is a broken citation. Saying so is the
// difference between a checked answer and a blank panel that looks checked.
test('an id the service will not return is reported, not rendered blank', async () => {
  fetchEvidence.mockResolvedValue({ evidence: [] })
  renderDrawer(999)

  expect(await screen.findByText(/引用不可用/)).toBeInTheDocument()
})

test('closing the drawer reports back', async () => {
  const user = userEvent.setup()
  const onClose = renderDrawer(7)
  await screen.findByText('Tabetomo')

  await user.click(screen.getByRole('button', { name: /close/i }))
  await waitFor(() => expect(onClose).toHaveBeenCalled())
})
