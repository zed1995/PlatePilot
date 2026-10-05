// candidate-cards.tsx answers "第二家" with a name.
//
// The candidate snapshot is what a follow-up pronoun resolves against, so the
// page has to show the same ordinal the agent used. Position is carried by the
// server and never derived from the array index — an off-by-one here silently
// renames the restaurant the user is asking about.
//
// Each row also carries why it ranked where it did and how old its data is.
// Both belong on the card rather than in a tooltip: a ranking whose reasons and
// observation date are hidden is an ordering the reader has to take on faith.
import type { CandidateView } from '../../api/types'
import { StatusTag } from '../status-tag'

export interface CandidateCardsProps {
  candidates: CandidateView[]
}

// dedupeByPosition keeps the first row of each position.
//
// The store has been observed writing one restaurant into two positions. Showing
// both would make "第三家" ambiguous in the one place that is supposed to
// resolve it, so the duplicate is dropped rather than the ordering rewritten:
// the first occurrence is the one the agent saw first.
export function dedupeByPosition(candidates: CandidateView[]): CandidateView[] {
  const seen = new Set<number>()
  const out: CandidateView[] = []
  for (const candidate of candidates) {
    if (seen.has(candidate.position)) continue
    seen.add(candidate.position)
    out.push(candidate)
  }
  return out.sort((a, b) => a.position - b.position)
}

export function CandidateCards({ candidates }: CandidateCardsProps) {
  const rows = dedupeByPosition(candidates)
  if (rows.length === 0) return null

  return (
    <section className="space-y-1.5" data-testid="candidate-cards">
      <p className="m-0 text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">
        本轮候选（追问里的「第 N 家」指这里）
      </p>
      <ul className="m-0 list-none space-y-1.5 p-0">
        {rows.map((candidate) => (
          <li
            key={candidate.position}
            className="flex flex-col gap-1 rounded-lg border border-[var(--border-subtle)] bg-surface-solid px-2.5 py-2"
          >
            <div className="flex items-center gap-2.5">
              <StatusTag tone="grey">#{candidate.position}</StatusTag>
              <span className="min-w-0 flex-1 truncate text-[12px] font-medium text-ink">
                {candidate.name ?? `餐厅 #${candidate.restaurant_id}`}
              </span>
              <span className="shrink-0 text-[11px] text-ink-tertiary tabular">
                id {candidate.restaurant_id}
              </span>
              {candidate.score !== undefined && (
                <span className="shrink-0 text-[11px] text-ink-tertiary tabular">
                  {candidate.score.toFixed(3)}
                </span>
              )}
            </div>
            {candidate.reasons && candidate.reasons.length > 0 && (
              <p className="m-0 pl-[42px] text-[11px] leading-snug text-ink-secondary">
                命中理由：{candidate.reasons.join('；')}
              </p>
            )}
            {candidate.snapshot_at && (
              <p className="m-0 pl-[42px] text-[10px] text-ink-tertiary">
                数据时间 {formatSnapshotDate(candidate.snapshot_at)}
              </p>
            )}
          </li>
        ))}
      </ul>
    </section>
  )
}

// formatSnapshotDate renders a snapshot timestamp as its calendar date.
//
// The date is what a reader compares against "is this still true", and the
// clock time of a batch run adds nothing to that judgement. An unparseable value
// is shown as-is rather than dropped: a date the reader cannot trust is still
// better than a row that claims to have no observation date at all.
export function formatSnapshotDate(raw: string): string {
  const parsed = new Date(raw)
  if (Number.isNaN(parsed.getTime())) return raw
  return parsed.toISOString().slice(0, 10)
}
