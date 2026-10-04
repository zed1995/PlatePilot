// gate-banner.tsx is the surface for a thread that is waiting on a person.
//
// Two of the ten stream events mean "this is your turn": awaiting_input and
// confirmation.required. Both are followed by a message.end and both leave the
// thread parked, so without a banner they are indistinguishable from an answer
// that finished — which is how a conversation dies without anyone noticing.
//
// The other thing this file does is refuse to speak the server's internal
// vocabulary. `missing_slots` carries names like `restaurant_id`, and that string
// has been read aloud to users verbatim. A slot name is a field on a struct; the
// banner says what the person actually has to tell us.
import { Check, Loader2, X } from 'lucide-react'

import type { ConfirmResponse } from '../../api/types'
import type { Gate } from '../../hooks/useAgentTurn'
import { Button } from '../ui/button'
import { cn } from '../../lib/utils'

// slotPhrases maps an internal slot to the thing a person would say.
//
// The fallback is deliberately vague rather than echoing the unknown name:
// "还需要你补充一项信息" is a worse sentence than a correct translation and a far
// better one than `party_size`.
const slotPhrases: Record<string, string> = {
  restaurant_id: '具体是哪一家店',
  restaurant_name: '店名',
  restaurant: '具体是哪一家店',
  borough: '在哪个区',
  neighborhood: '在哪个街区',
  cuisine: '想吃什么菜系',
  cuisines: '想吃什么菜系',
  price_level: '人均价位',
  min_rating: '最低评分',
  date: '哪一天',
  datetime: '哪一天几点',
  time: '几点',
  party_size: '几个人',
  location: '大概位置',
}

export function describeSlots(slots: string[] | undefined): string {
  const phrases: string[] = []
  for (const slot of slots ?? []) {
    const phrase = slotPhrases[slot]
    if (phrase) {
      if (!phrases.includes(phrase)) phrases.push(phrase)
      continue
    }
    if (!phrases.includes('一项补充信息')) phrases.push('一项补充信息')
  }
  return phrases.join('、')
}

// clarifySentence is the clarification line. It never contains a raw slot name —
// that is the one rule of this component, and it is the reason the function
// exists rather than the sentence being inlined.
export function clarifySentence(gate: Gate): string {
  const wanted = describeSlots(gate.missingSlots)
  if (!wanted) return '系统在等你的下一条消息。'
  return `系统在等你说明${wanted}。直接在下一条消息里回答就行。`
}

export interface GateBannerProps {
  awaiting?: Gate
  confirmation?: Gate
  onDecide: (decision: 'confirm' | 'cancel') => void
  deciding?: boolean
  outcome?: ConfirmResponse | null
  error?: string | null
}

export function GateBanner({
  awaiting,
  confirmation,
  onDecide,
  deciding = false,
  outcome,
  error,
}: GateBannerProps) {
  if (confirmation) {
    return (
      <div
        data-testid="gate-banner"
        className="mx-1 mb-2 rounded-xl border border-ink/15 bg-black/[0.03] px-3 py-2.5"
      >
        <p className="m-0 text-[13px] font-medium text-ink">需要你确认</p>
        <p className="m-0 mt-1 text-[12px] text-ink-secondary">
          {confirmation.summary ?? '系统准备执行一个写操作，需要你先确认。'}
        </p>
        <div className="mt-2 flex items-center gap-2">
          {/* Confirm is the write. It gets the primary colour and Cancel gets
              nothing louder than a border, so a double-click on a slow reply
              cannot silently mean "yes". */}
          <Button size="sm" onClick={() => onDecide('confirm')} disabled={deciding}>
            {deciding ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Check className="h-3.5 w-3.5" />}
            确认
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => onDecide('cancel')}
            disabled={deciding}
          >
            <X className="h-3.5 w-3.5" />
            取消
          </Button>
          {confirmation.pendingAction && (
            <span className="ml-auto font-mono text-[11px] text-ink-tertiary">
              {confirmation.pendingAction}
            </span>
          )}
        </div>

        {outcome && (
          <p
            className={cn(
              'm-0 mt-2 rounded-lg px-2.5 py-1.5 text-[11px]',
              outcome.replayed ? 'bg-warning/10 text-warning' : 'bg-black/[0.04] text-ink-secondary',
            )}
          >
            {outcome.replayed
              ? `这条已经处理过（幂等，未重复执行）：${outcome.message}`
              : outcome.message}
            <span className="ml-1 font-mono">state={outcome.state}</span>
          </p>
        )}
        {error && <p className="m-0 mt-2 text-[11px] text-error">{error}</p>}
      </div>
    )
  }

  if (awaiting) {
    return (
      <div
        data-testid="gate-banner"
        className="mx-1 mb-2 flex items-start gap-2 rounded-xl border border-warning/20 bg-warning/5 px-3 py-2.5"
      >
        <span className="mt-0.5 h-1.5 w-1.5 shrink-0 rounded-full bg-warning" />
        <p className="m-0 text-[12px] text-ink">
          {clarifySentence(awaiting)}
          <span className="ml-1 font-mono text-[11px] text-ink-tertiary">{awaiting.state}</span>
        </p>
      </div>
    )
  }

  return null
}
