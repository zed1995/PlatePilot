import { render, screen } from '@testing-library/react'

import type { CandidateView } from '../../api/types'
import { CandidateCards, dedupeByPosition } from './candidate-cards'

const rows: CandidateView[] = [
  { position: 1, restaurant_id: 11, name: 'Tabetomo', score: 0.91 },
  { position: 2, restaurant_id: 22, name: 'Kiraku', score: 0.88 },
  { position: 3, restaurant_id: 33, name: 'Bassanova', score: 0.8 },
]

// The store has been seen writing one restaurant into two positions. Rendering
// both would make "第三家" ambiguous in the one place that resolves it, and the
// first occurrence is the one the agent saw first.
test('dedupeByPosition keeps the first row per position and orders by it', () => {
  expect(
    dedupeByPosition([
      { position: 2, restaurant_id: 22 },
      { position: 1, restaurant_id: 11 },
      { position: 2, restaurant_id: 99 },
    ]).map((row) => row.restaurant_id),
  ).toEqual([11, 22])
})

test('the ordinal is the server’s position, and it is visible', () => {
  render(<CandidateCards candidates={rows} />)

  expect(screen.getByText('#1')).toBeInTheDocument()
  expect(screen.getByText('Tabetomo')).toBeInTheDocument()
  expect(screen.getByText('id 33')).toBeInTheDocument()
  // Position order, not array order — a follow-up pronoun resolves against this.
  const labels = screen.getAllByText(/^#\d$/).map((node) => node.textContent)
  expect(labels).toEqual(['#1', '#2', '#3'])
})

test('no candidates renders nothing rather than an empty card', () => {
  const { container } = render(<CandidateCards candidates={[]} />)
  expect(container).toBeEmptyDOMElement()
})
