import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'

import Inventory from './Inventory'

const inventoryResponse = {
  restaurant_id: 42,
  date: '2026-10-04',
  slots: [
    { slot_id: 's1', restaurant_id: 42, slot_date: '2026-10-04', slot_time: '19:00', capacity: 4, booked: 2, policy_version: 'v1', created_at: '2026-10-01T00:00:00Z' },
  ],
  reservations: [
    { reservation_id: 'r1', thread_id: 't1', user_id: 'u1', restaurant_id: 42, slot_id: 's1', party_size: 2, status: 'confirmed', idempotency_key: 'k1', created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z' },
  ],
}

vi.mock('../api/client', () => ({
  adminApi: {
    inventory: vi.fn(async () => inventoryResponse),
    resetInventory: vi.fn(async () => ({ restaurant_id: 42, slots_reset: 1, reservations_removed: 1 })),
  },
}))

import { adminApi } from '../api/client'

// Mock call records accumulate across tests; each asserts on its own calls.
beforeEach(() => vi.clearAllMocks())

test('loads inventory for a restaurant id and renders slots and reservations', async () => {
  const user = userEvent.setup()
  render(
    <MemoryRouter>
      <Inventory />
    </MemoryRouter>,
  )

  await user.type(screen.getByPlaceholderText('例如 42'), '42')

  await waitFor(() => {
    expect(adminApi.inventory).toHaveBeenCalledWith(42, undefined)
    expect(document.body).toHaveTextContent('19:00')
    expect(document.body).toHaveTextContent('r1')
  })
})

test('reset asks for confirmation and reports the result', async () => {
  const user = userEvent.setup()
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
  render(
    <MemoryRouter>
      <Inventory />
    </MemoryRouter>,
  )

  await user.type(screen.getByPlaceholderText('例如 42'), '42')
  await waitFor(() => expect(document.body).toHaveTextContent('19:00'))

  await user.click(screen.getByRole('button', { name: /重置库存/ }))

  await waitFor(() => {
    expect(confirm).toHaveBeenCalled()
    expect(adminApi.resetInventory).toHaveBeenCalledWith(42, undefined)
    expect(document.body).toHaveTextContent('已重置 1 个时段，删除 1 条预约')
  })
})

test('reset is skipped when the operator cancels the confirm', async () => {
  const user = userEvent.setup()
  vi.spyOn(window, 'confirm').mockReturnValue(false)
  render(
    <MemoryRouter>
      <Inventory />
    </MemoryRouter>,
  )

  await user.type(screen.getByPlaceholderText('例如 42'), '42')
  await waitFor(() => expect(document.body).toHaveTextContent('19:00'))

  await user.click(screen.getByRole('button', { name: /重置库存/ }))

  await waitFor(() => expect(confirm).toHaveBeenCalled())
  expect(adminApi.resetInventory).not.toHaveBeenCalled()
})
