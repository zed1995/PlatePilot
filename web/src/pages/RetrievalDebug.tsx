import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Card, CardContent, CardHeader, CardTitle } from '../components/ui/card'
import { Input } from '../components/ui/input'
import { Button } from '../components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../components/ui/table'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '../components/ui/dialog'
import { PageHeader } from '../components/page-header'
import { TracePanel } from '../components/trace-panel'
import { ErrorState } from '../components/error-state'
import { EmptyState } from '../components/empty-state'
import { adminApi } from '../api/client'
import type { Evidence, RestaurantCandidate, SearchRequest, SearchResponse } from '../api/types'

const boroughOptions = [
  { value: 'manhattan', label: 'Manhattan' },
  { value: 'brooklyn', label: 'Brooklyn' },
  { value: 'queens', label: 'Queens' },
  { value: 'bronx', label: 'Bronx' },
  { value: 'staten island', label: 'Staten Island' },
]

interface EvidenceState { restaurantName: string; loading: boolean; evidence: Evidence[]; error?: string }

export function RetrievalDebug() {
  const [query, setQuery] = useState('')
  const [text, setText] = useState('')
  const [borough, setBorough] = useState('')
  const [cuisine, setCuisine] = useState('')
  const [price, setPrice] = useState('')
  const [minRating, setMinRating] = useState('')
  const [topK, setTopK] = useState('5')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string>()
  const [result, setResult] = useState<SearchResponse>()
  const [evidence, setEvidence] = useState<EvidenceState>()

  const runSearch = async () => {
    const request: SearchRequest = {
      query: query.trim() || undefined,
      text: text.trim() || undefined,
      top_k: topK ? Number(topK) : undefined,
      filter: (borough || cuisine || minRating)
        ? {
            borough: borough || undefined,
            cuisines: cuisine ? [cuisine] : undefined,
            price_levels: price ? [Number(price)] : undefined,
            min_rating: minRating ? Number(minRating) : undefined,
          }
        : undefined,
    }
    setLoading(true); setError(undefined); setResult(undefined)
    try { setResult(await adminApi.debugSearch(request)) }
    catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setLoading(false) }
  }

  const openEvidence = async (c: RestaurantCandidate) => {
    setEvidence({ restaurantName: c.name, loading: true, evidence: [] })
    try {
      const bundle = await adminApi.debugEvidence({ restaurant_ids: [c.restaurant_id] })
      setEvidence({ restaurantName: c.name, loading: false, evidence: bundle.evidence })
    } catch (e) {
      setEvidence({ restaurantName: c.name, loading: false, evidence: [], error: e instanceof Error ? e.message : String(e) })
    }
  }

  return (
    <div className="space-y-5">
      <PageHeader title="Retrieval debug" description="Probe the serving retrieval path: candidates, channel scoring, and trace." />
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-3">
        <Card>
          <CardHeader><CardTitle>Request</CardTitle></CardHeader>
          <CardContent className="space-y-3">
            <Field label="Query — a whole natural-language question">
              <Input placeholder="where should I go for a date?" value={query} onChange={(e) => setQuery(e.target.value)} />
            </Field>
            <Field label="Text — a restaurant name or address fragment">
              <Input placeholder="Joe's Pizza, Carmine St" value={text} onChange={(e) => setText(e.target.value)} />
            </Field>
            <Field label="Borough">
              <Select value={borough} onValueChange={setBorough}>
                <SelectTrigger><SelectValue placeholder="any" /></SelectTrigger>
                <SelectContent>{boroughOptions.map((o) => <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>)}</SelectContent>
              </Select>
            </Field>
            <Field label="Cuisine">
              <Input placeholder="italian" value={cuisine} onChange={(e) => setCuisine(e.target.value)} />
            </Field>
            <div className="grid grid-cols-3 gap-2">
              <Field label="Price"><Input type="number" min={1} max={4} value={price} onChange={(e) => setPrice(e.target.value)} /></Field>
              <Field label="Min rating"><Input type="number" min={0} max={5} step={0.1} value={minRating} onChange={(e) => setMinRating(e.target.value)} /></Field>
              <Field label="Top K"><Input type="number" min={1} max={50} value={topK} onChange={(e) => setTopK(e.target.value)} /></Field>
            </div>
            <Button onClick={runSearch} disabled={loading} className="w-full">{loading ? 'Running…' : 'Run retrieval'}</Button>
            <p className="text-[12px] text-ink-tertiary">Channels and weights come from the service configuration and cannot be changed here.</p>
          </CardContent>
        </Card>
        <div className="space-y-3 lg:col-span-2">
          {error && <ErrorState title="Retrieval failed" description={error} onRetry={runSearch} />}
          {!result && !error && !loading && <EmptyState title="Submit a request" description="Inspect the serving retrieval path" />}
          {result && (
              <div className="space-y-3">
                <Card>
                  <CardHeader><CardTitle>Candidates ({result.candidates.length})</CardTitle></CardHeader>
                  <CardContent>
                    <Table>
                      <TableHeader>
                        <TableRow><TableHead>Restaurant</TableHead><TableHead>Score</TableHead><TableHead>Rating</TableHead><TableHead>Reasons</TableHead><TableHead /></TableRow>
                      </TableHeader>
                      <TableBody>
                        {result.candidates.map((c) => (
                          <TableRow key={c.restaurant_id}>
                            <TableCell>
                              <Link className="text-ink hover:underline" to={`/restaurants/${c.restaurant_id}`}>{c.name}</Link>
                              <div className="text-[12px] text-ink-tertiary">{c.address ?? ''}</div>
                            </TableCell>
                            <TableCell className="tabular">{c.score.toFixed(3)}</TableCell>
                            <TableCell className="tabular">{c.rating ?? '-'}</TableCell>
                            <TableCell className="text-ink-secondary">{c.reasons?.join(' · ') ?? '-'}</TableCell>
                            <TableCell><Button size="sm" variant="outline" onClick={() => openEvidence(c)}>Evidence</Button></TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  </CardContent>
                </Card>
                <Card>
                  <CardHeader><CardTitle>Trace</CardTitle></CardHeader>
                  <CardContent>
                    {result.trace ? <TracePanel trace={result.trace} /> : <EmptyState title="no trace returned" />}
                  </CardContent>
                </Card>
              </div>
            )}
        </div>
      </div>

      <Dialog open={Boolean(evidence)} onOpenChange={(o: boolean) => !o && setEvidence(undefined)}>
        <DialogContent className="max-w-3xl">
          <DialogHeader><DialogTitle>Evidence — {evidence?.restaurantName ?? ''}</DialogTitle></DialogHeader>
          {evidence?.error && <ErrorState title="Failed to load evidence" description={evidence.error} />}
          {evidence?.loading ? <div className="py-10 text-center text-[12px] text-ink-tertiary">Loading…</div> : (
            <div className="space-y-3">
              {(evidence?.evidence ?? []).map((e) => (
                <Card key={e.evidence_id}>
                  <CardHeader><CardTitle>{e.doc_type}{e.title ? ` · ${e.title}` : ''}</CardTitle></CardHeader>
                  <CardContent>
                    <p className="whitespace-pre-wrap text-[13px]">{e.content}</p>
                    {e.source_record_ids?.length ? <p className="mt-2 text-[12px] text-ink-tertiary">{e.source_record_ids.join(' · ')}</p> : null}
                  </CardContent>
                </Card>
              ))}
              {evidence && !evidence.loading && evidence.evidence.length === 0 && <EmptyState title="no evidence returned" />}
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <label className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">{label}</label>
      {children}
    </div>
  )
}

export default RetrievalDebug