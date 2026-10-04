// answer.ts turns an assistant message into the few blocks the console renders.
//
// The model answers in a fixed five-part shape — 结论, 匹配原因, 来源与数据时间,
// 无法确认, FOLLOWUPS — and every one of those is plain text except for the [^n]
// footnote markers, which have to become something clickable. That is all this
// module does; there is no markdown, because adding a markdown parser to render
// five headings would be a dependency and a whole new escaping surface for a
// text shape we control.

export type AnswerToken =
  | { kind: 'text'; text: string }
  | { kind: 'citation'; evidenceId: number; label: string }

export interface AnswerSegment {
  kind: 'paragraph' | 'list'
  lines: string[]
}

export interface ParsedAnswer {
  segments: AnswerSegment[]
  // followups are the model's own suggestions, rendered as chips the user can
  // send verbatim: asking a follow-up is how the console demonstrates that a
  // thread keeps its context.
  followups: string[]
}

// citationMarkers matches an occurrence of [^n]. It is global so one paragraph
// carrying several footnotes yields several tokens, and it is anchored on the
// caret so a bracketed number meant as a literal stays text.
const citationMarkers = /\[\^(\d+)\]/g

// tokenizeAnswer splits one line into renderable pieces.
export function tokenizeAnswer(line: string): AnswerToken[] {
  const tokens: AnswerToken[] = []
  let last = 0
  for (const match of line.matchAll(citationMarkers)) {
    const at = match.index ?? 0
    if (at > last) tokens.push({ kind: 'text', text: line.slice(last, at) })
    tokens.push({
      kind: 'citation',
      evidenceId: Number(match[1]),
      label: match[1],
    })
    last = at + match[0].length
  }
  if (last < line.length) tokens.push({ kind: 'text', text: line.slice(last) })
  return tokens
}

// collectCitationIds returns every footnote id in an answer, in first-seen
// order. The console uses it to know which footnotes it can honour: a marker
// with no matching evidence is a dead link, and a citation drawer that opened
// empty would look identical to one that opened correctly.
export function collectCitationIds(content: string): number[] {
  const ids: number[] = []
  const seen = new Set<number>()
  for (const match of content.matchAll(citationMarkers)) {
    const id = Number(match[1])
    if (seen.has(id)) continue
    seen.add(id)
    ids.push(id)
  }
  return ids
}

// parseAnswer turns raw assistant text into paragraphs the renderer walks.
//
// Lines beginning with -, *, or • become one list segment; a blank line starts a
// new paragraph; FOLLOWUPS: starts the suggestion block the console turns into
// buttons, which is why everything after it leaves the paragraph flow.
export function parseAnswer(content: string): ParsedAnswer {
  const segments: AnswerSegment[] = []
  const followups: string[] = []
  let collectingFollowups = false
  let open: AnswerSegment | null = null

  for (const rawLine of (content ?? '').split('\n')) {
    const line = rawLine.trimEnd()
    const trimmed = line.trim()
    if (trimmed === '') {
      open = null
      continue
    }
    if (/^FOLLOWUPS?\s*[:：]\s*/i.test(trimmed)) {
      collectingFollowups = true
      const inline = trimmed.replace(/^FOLLOWUPS?\s*[:：]\s*/i, '').trim()
      if (inline) followups.push(...splitFollowups(inline))
      continue
    }
    if (collectingFollowups) {
      followups.push(...splitFollowups(trimmed))
      continue
    }
    const bullet = trimmed.replace(/^[-*•]\s+/, '')
    const isList = bullet !== trimmed
    if (!open || open.kind !== (isList ? 'list' : 'paragraph')) {
      open = { kind: isList ? 'list' : 'paragraph', lines: [] }
      segments.push(open)
    }
    open.lines.push(isList ? bullet : trimmed)
  }
  return { segments, followups }
}

// splitFollowups accepts both a numbered list and a separated line, because the
// model emits whichever it pleases and either shape is one suggestion.
function splitFollowups(line: string): string[] {
  const numbered = line.replace(/^\d+[.)、]\s*/, '').trim()
  if (!numbered) return []
  return numbered.split(/[|;；]|\s+·\s+/).map((s) => s.trim()).filter(Boolean)
}
