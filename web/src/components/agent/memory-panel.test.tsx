import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import type { MemoryView } from '../../api/types'
import { MemoryPanel } from './memory-panel'

const api = vi.hoisted(() => ({
  listMemories: vi.fn(),
  updateMemory: vi.fn(),
  deleteMemory: vi.fn(),
}))

vi.mock('../../api/chat', () => ({
  chatApi: {
    listMemories: api.listMemories,
    updateMemory: api.updateMemory,
    deleteMemory: api.deleteMemory,
  },
  getUserId: () => 'demo-user',
  setUserId: vi.fn(),
}))

const disliked: MemoryView = {
  id: 'mem-1',
  memory_type: 'dietary',
  content: '不吃辣',
  source: 'conversation',
  confidence: 0.9,
  created_at: '2026-10-01T00:00:00Z',
  updated_at: '2026-10-01T00:00:00Z',
}

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryPanel />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  api.listMemories.mockReset()
  api.listMemories.mockResolvedValue({ memories: [disliked] })
  api.updateMemory.mockReset()
  api.updateMemory.mockResolvedValue(disliked)
  api.deleteMemory.mockReset()
  api.deleteMemory.mockResolvedValue(undefined)
})

test('lists what the agent remembers, with its provenance', async () => {
  renderPanel()

  expect(await screen.findByText('不吃辣')).toBeInTheDocument()
  expect(screen.getByText('dietary')).toBeInTheDocument()
  expect(screen.getByText(/source=conversation/)).toBeInTheDocument()
})

// PATCH is "at least one field". Sending the untouched field back would be a
// write the user did not ask for, and would restamp updated_at on a row nobody
// edited.
test('editing the content sends the content and nothing else', async () => {
  const user = userEvent.setup()
  renderPanel()

  await user.click(await screen.findByRole('button', { name: /改/ }))
  const content = screen.getByLabelText('记忆内容')
  await user.clear(content)
  await user.type(content, '不吃辣，也不吃香菜')
  await user.click(screen.getByRole('button', { name: /保存/ }))

  await waitFor(() =>
    expect(api.updateMemory).toHaveBeenCalledWith('mem-1', {
      content: '不吃辣，也不吃香菜',
    }),
  )
})

test('editing the type sends the type and nothing else', async () => {
  const user = userEvent.setup()
  renderPanel()

  await user.click(await screen.findByRole('button', { name: /改/ }))
  const type = screen.getByLabelText('memory_type')
  await user.clear(type)
  await user.type(type, 'avoid')
  await user.click(screen.getByRole('button', { name: /保存/ }))

  await waitFor(() =>
    expect(api.updateMemory).toHaveBeenCalledWith('mem-1', { memory_type: 'avoid' }),
  )
})

test('saving with nothing changed is refused rather than sent as a no-op', async () => {
  const user = userEvent.setup()
  renderPanel()

  await user.click(await screen.findByRole('button', { name: /改/ }))
  expect(screen.getByRole('button', { name: /保存/ })).toBeDisabled()
  expect(api.updateMemory).not.toHaveBeenCalled()
})

// Deleting a memory is not undoable, so it goes through a dialog instead of
// firing on the row button.
test('deleting asks first, then removes the row', async () => {
  const user = userEvent.setup()
  renderPanel()

  await user.click(await screen.findByRole('button', { name: /删/ }))
  expect(api.deleteMemory).not.toHaveBeenCalled()

  api.listMemories.mockResolvedValue({ memories: [] })
  await user.click(screen.getByRole('button', { name: '删除' }))

  await waitFor(() => expect(api.deleteMemory).toHaveBeenCalledWith('mem-1'))
  await waitFor(() => expect(screen.getByText('还没有记忆')).toBeInTheDocument())
})

test('an empty list says how to get one', async () => {
  api.listMemories.mockResolvedValue({ memories: [] })
  renderPanel()

  expect(await screen.findByText('还没有记忆')).toBeInTheDocument()
  expect(screen.getByText(/记住我不吃辣/)).toBeInTheDocument()
})
