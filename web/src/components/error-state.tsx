import { AlertCircle } from 'lucide-react'
import { Button } from './ui/button'

export function ErrorState({ title, description, onRetry }: { title: string; description?: string; onRetry?: () => void }) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 py-12 text-center">
      <div className="flex h-10 w-10 items-center justify-center rounded-full bg-error/10 text-error">
        <AlertCircle className="h-5 w-5" />
      </div>
      <div>
        <p className="text-[13px] font-medium text-ink">{title}</p>
        {description && <p className="mt-1 text-[12px] text-ink-tertiary">{description}</p>}
      </div>
      {onRetry && <Button variant="outline" size="sm" onClick={onRetry}>Retry</Button>}
    </div>
  )
}