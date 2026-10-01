import { render, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'

import Restaurants from './Restaurants'

vi.mock('../api/client', () => ({
  adminApi: {
    restaurants: vi.fn(async () => ({
      items: [
        {
          restaurant_id: 123,
          name: 'Joe',
          borough: 'manhattan',
          cuisines: ['pizza'],
          rating_count: 10,
          observed_at: '2021-09-01T00:00:00Z',
        },
      ],
    })),
  },
}))

test('renders the restaurant listing', async () => {
  render(
    <MemoryRouter>
      <Restaurants />
    </MemoryRouter>,
  )

  await waitFor(() => expect(document.body).toHaveTextContent('Joe'))
  expect(document.body).toHaveTextContent('pizza')
})

test('applies a borough filter', async () => {
  const { adminApi } = await import('../api/client')
  render(
    <MemoryRouter>
      <Restaurants />
    </MemoryRouter>,
  )

  // Select Manhattan and submit the filter form.
  const select = document.querySelector('.ant-select-selector')!
  await userEvent.click(select)
  const option = await waitFor(() =>
    document.querySelector<HTMLElement>('.ant-select-item-option')!,
  )
  await userEvent.click(option)
  await userEvent.click(document.querySelector('button[type="submit"]')!)

  await waitFor(() =>
    expect(adminApi.restaurants).toHaveBeenCalledWith(
      expect.objectContaining({ borough: 'manhattan' }),
    ),
  )
})
