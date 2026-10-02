import { Card, CardContent } from './ui/card'
import { cn } from '../lib/utils'

interface StatCardProps {
  label: string
  value: React.ReactNode
  suffix?: React.ReactNode
  loading?: boolean
  tone?: 'success' | 'warning' | 'error'
}

export function StatCard({ label, value, suffix, loading, tone }: StatCardProps) {
  const valueClass = cn(
    'text-[24px] font-semibold tabular tracking-tight',
    tone === 'success' && 'text-success',
    tone === 'warning' && 'text-warning',
    tone === 'error' && 'text-error',
    !tone && 'text-ink',
  )
  return (
    <Card className={loading ? 'opacity-60' : undefined}>
      <CardContent className="pt-5">
        <div className="text-[11px] text-ink-tertiary">{label}</div>
        <div className="mt-2 flex items-baseline gap-1.5">
          <span className={valueClass}>{loading ? '—' : value}</span>
          {suffix && <span className="text-[12px] text-ink-tertiary">{suffix}</span>}
        </div>
      </CardContent>
    </Card>
  )
}