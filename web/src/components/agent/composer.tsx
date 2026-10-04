// composer.tsx is the input at the bottom of the transcript.
//
// Enter sends and Shift+Enter breaks the line, which is the convention a chat
// box has trained everyone to expect — with one exception that matters here.
// Chinese text is composed through an IME, and the Enter that commits a
// candidate word is the same key that would otherwise send the message. Sending
// mid-composition would post half a sentence, so composition state is checked
// before the key is honoured.
import { useState, type KeyboardEvent } from 'react'
import { ArrowUp, Square } from 'lucide-react'

import { Button } from '../ui/button'
import { Textarea } from '../ui/textarea'

// contentLimit mirrors the server's 1–4000 rune bound. Refusing an over-long
// message here costs a keystroke; letting it through costs a round trip that
// ends in a 400 the user has to read to understand.
export const contentLimit = 4000

export interface ComposerProps {
  onSend: (content: string) => void
  onStop: () => void
  running: boolean
  disabled?: boolean
  placeholder?: string
}

export function Composer({
  onSend,
  onStop,
  running,
  disabled = false,
  placeholder = '说一句像「布鲁克林 4 星以上的拉面，要安静，推荐 3 家并说明依据」的话',
}: ComposerProps) {
  const [draft, setDraft] = useState('')
  const trimmed = draft.trim()
  const tooLong = draft.length > contentLimit
  const canSend = !running && !disabled && trimmed.length > 0 && !tooLong

  function submit() {
    if (!canSend) return
    onSend(trimmed)
    setDraft('')
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key !== 'Enter' || event.shiftKey) return
    // isComposing is the IME guard: committing a candidate word also arrives as
    // a plain Enter keydown, and sending there would post half a sentence.
    if (event.nativeEvent.isComposing) return
    event.preventDefault()
    submit()
  }

  return (
    <div className="border-t border-[var(--border-subtle)] px-1 pt-3">
      <div className="relative">
        <Textarea
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={onKeyDown}
          placeholder={placeholder}
          disabled={disabled || running}
          aria-label="消息输入"
          className="min-h-[64px] resize-none pr-28"
        />
        <div className="absolute bottom-2.5 right-2.5 flex items-center gap-1.5">
          <span className="text-[11px] text-ink-tertiary tabular">
            {draft.length}/{contentLimit}
          </span>
          {running ? (
            <Button type="button" variant="outline" size="sm" onClick={onStop}>
              <Square className="h-3 w-3" />
              停止
            </Button>
          ) : (
            <Button type="button" size="sm" onClick={submit} disabled={!canSend}>
              <ArrowUp className="h-3.5 w-3.5" />
              发送
            </Button>
          )}
        </div>
      </div>
      <p className="m-0 px-1 pt-1.5 text-[11px] text-ink-tertiary">
        {tooLong ? `超出上限，请删到 ${contentLimit} 字以内。` : 'Enter 发送，Shift+Enter 换行。'}
      </p>
    </div>
  )
}
