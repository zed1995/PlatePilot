import type { ReactNode } from 'react'
import { Badge } from './ui/badge'

export type StatusTone = 'green' | 'blue' | 'orange' | 'red' | 'grey'

const toneVariant: Record<StatusTone, 'success' | 'info' | 'warning' | 'destructive' | 'secondary'> = {
  green: 'success',
  blue: 'info',
  orange: 'warning',
  red: 'destructive',
  grey: 'secondary',
}

export function StatusTag({ tone = 'grey', children }: { tone?: StatusTone; children: ReactNode }) {
  return <Badge variant={toneVariant[tone]}>{children}</Badge>
}