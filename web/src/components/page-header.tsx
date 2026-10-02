import * as React from 'react'
import { cn } from '../lib/utils'

interface PageHeaderProps {
  title: React.ReactNode
  description?: React.ReactNode
  extra?: React.ReactNode
}

export function PageHeader({ title, description, extra }: PageHeaderProps) {
  return (
    <header className="mb-7 flex items-start justify-between gap-4">
      <div>
        <h1 className="text-[22px] font-semibold tracking-tight text-ink">{title}</h1>
        {description && <p className="mt-1 text-[12px] text-ink-tertiary">{description}</p>}
      </div>
      {extra && <div className={cn('flex items-center gap-2')}>{extra}</div>}
    </header>
  )
}