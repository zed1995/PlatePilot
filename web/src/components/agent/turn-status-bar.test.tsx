import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { initialTurnState, type TurnState } from '../../hooks/useAgentTurn'
import { TurnStatusBar, warningPreviewLimit } from './turn-status-bar'

function turnWithWarnings(warnings: string[]): TurnState {
  return { ...initialTurnState, phase: 'done', warnings }
}

// Warnings must be surfaced (the vector-channel degradation appears nowhere
// else), but a turn that collected dozens of per-entity notes must not push the
// whole transcript off screen. The first few stay open; the rest fold behind a
// single disclosure rather than rendering a wall of yellow lines.
test('a handful of warnings are all shown without a fold control', () => {
  render(<TurnStatusBar turn={turnWithWarnings(['向量通道不可用', '证据退化为顺序排序'])} />)

  expect(screen.getByText('向量通道不可用')).toBeInTheDocument()
  expect(screen.getByText('证据退化为顺序排序')).toBeInTheDocument()
  expect(screen.queryByText(/展开其余/)).not.toBeInTheDocument()
})

test('warnings past the preview limit fold behind one disclosure, and still exist in the DOM', () => {
  const warnings = Array.from({ length: warningPreviewLimit + 3 }, (_, i) => `第 ${i + 1} 条提示`)
  render(<TurnStatusBar turn={turnWithWarnings(warnings)} />)

  expect(screen.getByText('第 1 条提示')).toBeInTheDocument()
  // Folded, not dropped: a hidden-in-a-closed-<details> warning is still
  // queryable and readable once expanded.
  expect(screen.getByText(`第 ${warningPreviewLimit + 3} 条提示`)).toBeInTheDocument()
  expect(screen.getByText(`展开其余 3 条`)).toBeInTheDocument()
})

test('expanding the fold reveals every warning', async () => {
  const user = userEvent.setup()
  const warnings = Array.from({ length: warningPreviewLimit + 1 }, (_, i) => `提示 ${i + 1}`)
  render(<TurnStatusBar turn={turnWithWarnings(warnings)} />)

  await user.click(screen.getByText(`展开其余 1 条`))
  expect(screen.getByText(`提示 ${warningPreviewLimit + 1}`)).toBeVisible()
})
