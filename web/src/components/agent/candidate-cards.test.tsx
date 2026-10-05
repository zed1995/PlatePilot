import { render, screen } from '@testing-library/react'

import type { CandidateView } from '../../api/types'
import { CandidateCards, dedupeByPosition, formatSnapshotDate } from './candidate-cards'

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

// A ranking whose reasons and observation date are hidden is an ordering the
// reader has to take on faith. Both are shown on the card.
test('a row shows why it ranked and how old its data is', () => {
  render(
    <CandidateCards
      candidates={[
        {
          position: 1,
          restaurant_id: 11,
          name: 'Tabetomo',
          score: 0.91,
          reasons: ['评论推断：安静（ambience）', '硬条件命中：菜系=ramen'],
          snapshot_at: '2021-09-01T12:00:00Z',
        },
      ]}
    />,
  )

  expect(screen.getByText(/命中理由：评论推断：安静（ambience）；硬条件命中：菜系=ramen/)).toBeInTheDocument()
  expect(screen.getByText('数据时间 2021-09-01')).toBeInTheDocument()
})

// The field is absent when the store never recorded a snapshot, and an absent
// field must not be rendered as the zero timestamp.
test('a row without a snapshot shows no data time', () => {
  render(<CandidateCards candidates={[{ position: 1, restaurant_id: 11, name: 'Tabetomo' }]} />)

  expect(screen.queryByText(/数据时间/)).not.toBeInTheDocument()
})

test('formatSnapshotDate falls back to the raw value when it cannot be parsed', () => {
  expect(formatSnapshotDate('2021-09-01T12:00:00Z')).toBe('2021-09-01')
  expect(formatSnapshotDate('not-a-date')).toBe('not-a-date')
})
