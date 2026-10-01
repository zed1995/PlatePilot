import { render } from '@testing-library/react'

import TracePanel from './TracePanel'
import type { Trace } from '../api/types'

const trace: Trace = {
  candidate_pool: 3,
  returned: 2,
  top_k: 5,
  rerank_applied: false,
  channels: [
    { channel: 'structured', ran: true, weight: 1, results: 2 },
    { channel: 'vector', ran: false, weight: 1, results: 0, note: 'no embedding provider' },
  ],
  candidates: [
    {
      restaurant_id: 42,
      total: 0.9,
      channels: [
        {
          channel: 'structured',
          raw_score: 1,
          weight: 1,
          normalized_score: 1,
          contribution: 0.5,
          reason: 'hard filter',
        },
      ],
    },
  ],
  warnings: ['vector channel degraded'],
}

test('shows degradation warnings', () => {
  render(<TracePanel trace={trace} />)
  expect(document.body).toHaveTextContent('vector channel degraded')
})

test('shows the run summary counters', () => {
  render(<TracePanel trace={trace} />)
  expect(document.body).toHaveTextContent('Candidate pool')
  expect(document.body).toHaveTextContent('3')
  expect(document.body).toHaveTextContent('Returned')
})

test('shows channel activity including a skipped channel', () => {
  render(<TracePanel trace={trace} />)
  expect(document.body).toHaveTextContent('structured')
  expect(document.body).toHaveTextContent('no embedding provider')
})
