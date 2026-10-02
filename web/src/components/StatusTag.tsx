import type { CSSProperties, ReactNode } from 'react'

export type StatusTone = 'green' | 'blue' | 'orange' | 'red' | 'grey'

// Status renders statuses as quiet plain text, Apple-settings style: regular
// states use normal text, neutral states use muted grey, and only failures
// earn a colour. Tones are kept as the API so call sites read semantically.
const toneStyle: Record<StatusTone, CSSProperties | undefined> = {
  green: undefined,
  blue: undefined,
  orange: undefined,
  red: { color: '#ff3b30' },
  grey: { color: '#6e6e73' },
}

export default function StatusTag({
  tone = 'grey',
  children,
}: {
  tone?: StatusTone
  children: ReactNode
}) {
  return <span style={toneStyle[tone]}>{children}</span>
}
