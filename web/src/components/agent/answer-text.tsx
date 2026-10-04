// answer-text.tsx renders one assistant message.
//
// The model answers in a fixed five-part shape and the only markup inside it is
// the [^n] footnote. So this renders text, list items and clickable footnotes —
// nothing else. Adding a markdown renderer to style five headings would be a
// dependency, a new escaping surface, and a licence to render whatever the model
// invents; the structured parser in lib/answer.ts is the smaller commitment.
import { Fragment } from 'react'

import { parseAnswer, tokenizeAnswer } from '../../lib/answer'

export interface AnswerTextProps {
  content: string
  // citable is the evidence ids the server said were quotable this turn. A
  // footnote outside it is a dead link, and saying so is better than opening an
  // empty drawer that looks identical to a working one.
  citable?: number[]
  onCitationClick?: (evidenceId: number) => void
}

export function AnswerText({ content, citable, onCitationClick }: AnswerTextProps) {
  const { segments, followups } = parseAnswer(content)
  const available = citable ? new Set(citable) : null

  return (
    <div className="space-y-2 text-[13px] leading-[1.7] text-ink">
      {segments.map((segment, index) =>
        segment.kind === 'list' ? (
          <ul key={index} className="m-0 list-disc space-y-1 pl-5">
            {segment.lines.map((line, lineIndex) => (
              <li key={lineIndex}>
                <Line content={line} available={available} onCitationClick={onCitationClick} />
              </li>
            ))}
          </ul>
        ) : (
          <p key={index} className="m-0">
            {segment.lines.map((line, lineIndex) => (
              <Fragment key={lineIndex}>
                {lineIndex > 0 && <br />}
                <Line content={line} available={available} onCitationClick={onCitationClick} />
              </Fragment>
            ))}
          </p>
        ),
      )}
      {followups.length > 0 && (
        <div className="flex flex-wrap gap-1.5 pt-1">
          {followups.map((item) => (
            <span
              key={item}
              className="rounded-lg border border-[var(--border-subtle)] bg-black/[0.02] px-2 py-1 text-[11px] text-ink-secondary"
            >
              {item}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}

function Line({
  content,
  available,
  onCitationClick,
}: {
  content: string
  available: Set<number> | null
  onCitationClick?: (evidenceId: number) => void
}) {
  return (
    <>
      {tokenizeAnswer(content).map((token, index) => {
        if (token.kind === 'text') return <Fragment key={index}>{token.text}</Fragment>
        const known = !available || available.has(token.evidenceId)
        return (
          <sup key={index}>
            <button
              type="button"
              data-citation-id={token.evidenceId}
              disabled={!known || !onCitationClick}
              title={known ? `引用 #${token.label}` : '引用不可用：本轮证据里没有这条'}
              onClick={() => onCitationClick?.(token.evidenceId)}
              className={
                known
                  ? 'mx-0.5 rounded bg-black/[0.05] px-1 align-super text-[10px] font-medium text-ink-secondary hover:bg-black/[0.10]'
                  : 'mx-0.5 rounded bg-error/10 px-1 align-super text-[10px] font-medium text-error line-through'
              }
            >
              {token.label}
            </button>
          </sup>
        )
      })}
    </>
  )
}
