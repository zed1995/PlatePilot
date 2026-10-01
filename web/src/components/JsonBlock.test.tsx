import { render } from '@testing-library/react'

import JsonBlock from './JsonBlock'

test('renders an object as indented JSON', () => {
  render(<JsonBlock value={{ name: 'Joe', tags: ['pizza'] }} />)
  const text = document.querySelector('pre')?.textContent
  expect(text).toContain('"name": "Joe"')
  expect(text).toContain('"tags": [')
})

test('renders a string value unchanged', () => {
  render(<JsonBlock value="raw text" />)
  expect(document.querySelector('pre')?.textContent).toBe('raw text')
})

test('renders empty values as an empty box', () => {
  render(<JsonBlock value={null} />)
  expect(document.querySelector('pre')?.textContent).toBe('')
})
