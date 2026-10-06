import { AlertTriangle } from 'lucide-react'
import { Card, CardContent, CardHeader, CardTitle } from './ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from './ui/table'
import type { ChannelSummary, CandidateScore, Trace } from '../api/types'

function Meta({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col">
      <span className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">{label}</span>
      <span className="text-[13px] text-ink">{children}</span>
    </div>
  )
}

export function TracePanel({ trace }: { trace: Trace }) {
  return (
    <div className="space-y-3">
      {trace.warnings && trace.warnings.length > 0 && (
        <div className="flex items-start gap-2 rounded-lg border border-warning/20 bg-warning/5 p-3 text-[13px] text-warning">
          <AlertTriangle className="mt-0.5 h-4 w-4" />
          <ul className="m-0 list-disc pl-4">{trace.warnings.map((w) => <li key={w}>{w}</li>)}</ul>
        </div>
      )}
      <Card>
        <CardContent className="pt-5">
          <div className="grid grid-cols-2 gap-4 md:grid-cols-3">
            <Meta label="Candidate pool">{trace.candidate_pool}</Meta>
            <Meta label="Returned">{trace.returned}</Meta>
            <Meta label="Top K">{trace.top_k}</Meta>
            <Meta label="Embedding model">{trace.embedding_model_id ?? '-'}</Meta>
            <Meta label="Query dim">{trace.query_embedding_dim ?? '-'}</Meta>
            <Meta label="Rerank">
              {trace.rerank_applied ? `applied (${trace.rerank_model_id})`
                : trace.rerank_model_id ? `failed (${trace.rerank_model_id})` : 'off'}
            </Meta>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle>Channels</CardTitle></CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow><TableHead>Channel</TableHead><TableHead>Ran</TableHead><TableHead>Weight</TableHead><TableHead>Results</TableHead><TableHead>Note</TableHead></TableRow>
            </TableHeader>
            <TableBody>
              {(trace.channels ?? []).map((c: ChannelSummary) => (
                <TableRow key={c.channel}>
                  <TableCell>{c.channel}</TableCell>
                  <TableCell>{c.ran ? 'yes' : 'no'}</TableCell>
                  <TableCell>{c.weight}</TableCell>
                  <TableCell>{c.results}</TableCell>
                  <TableCell>
                    {c.note}
                    {c.dropped_restaurant_ids && c.dropped_restaurant_ids.length > 0 && (
                      // This is the per-id detail the user-facing warning
                      // deliberately aggregates away: an operator diagnosing a
                      // stale recall index needs the actual ids.
                      <span
                        className="mt-1 block font-mono text-[11px] text-ink-tertiary"
                        data-testid="dropped-restaurant-ids"
                      >
                        已忽略候选池外 {c.dropped_restaurant_ids.length} 家：
                        {c.dropped_restaurant_ids.join(', ')}
                      </span>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle>Candidates</CardTitle></CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow><TableHead>Restaurant ID</TableHead><TableHead>Total</TableHead></TableRow>
            </TableHeader>
            <TableBody>
              {(trace.candidates ?? []).map((c: CandidateScore) => (
                <TableRow key={c.restaurant_id}>
                  <TableCell>{c.restaurant_id}</TableCell>
                  <TableCell className="tabular">{c.total}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  )
}