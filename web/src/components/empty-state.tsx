import { Inbox } from 'lucide-react'
import type { ReactNode } from 'react'

export function EmptyState({ title, description, action }: { title: string; description?: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 py-16 text-center">
      <div className="flex h-10 w-10 items-center justify-center rounded-full bg-black/[0.04] text-ink-tertiary">
        <Inbox className="h-5 w-5" />
      </div>
      <div>
        <p className="text-[13px] font-medium text-ink">{title}</p>
        {description && <p className="mt-1 text-[12px] text-ink-tertiary">{description}</p>}
      </div>
      {action}
    </div>
  )
}