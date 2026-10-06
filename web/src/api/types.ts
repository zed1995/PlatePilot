// Types mirror the backend wire DTOs one to one. Field names are the exact
// JSON names the service emits; do not rename without changing the backend.

// --- Dashboard -------------------------------------------------------------

export interface TableCounts {
  restaurants_total: number
  restaurants_active: number
  reviews_estimate: number
  reviews_estimated: boolean
  documents_active: number
  batches_total: number
}

export interface DocumentCount {
  retrieval_scope: string
  doc_type: string
  count: number
}

export interface Migration {
  version: string
  applied_at: string
}

export interface Environment {
  embedding_model: string
  embedding_dimensions: number
}

export interface Overview {
  tables: TableCounts
  document_breakdown: DocumentCount[]
  active_documents_without_vector: number
  recent_batches: BatchListItem[]
  migrations: Migration[]
  environment: Environment
}

// --- Restaurants ------------------------------------------------------------

export interface RestaurantListItem {
  restaurant_id: number
  name: string
  address?: string
  borough?: string
  cuisines: string[]
  price_level?: number
  rating_computed_avg?: number
  rating_count: number
  is_active_for_demo: boolean
  knowledge_score: number
  observed_at: string
}

export interface RestaurantPage {
  items: RestaurantListItem[]
  next_cursor?: string
  total_estimate: number
}

export interface RestaurantDetail {
  restaurant_id: number
  source: string
  source_record_id: string
  name: string
  address?: string
  borough?: string
  longitude?: number
  latitude?: number
  description?: string

  categories: string[]
  cuisines: string[]

  price_raw?: string
  price_level?: number

  rating_source_avg?: number
  rating_computed_avg?: number
  rating_count: number

  source_review_count: number
  source_review_count_capped: boolean
  stored_review_count: number
  text_review_count: number
  representative_review_count: number
  embedded_review_count: number
  last_reviewed_at?: string
  stats_updated_at?: string

  // Opaque JSON objects rendered by the JSON viewer.
  attributes: unknown
  hours: unknown

  is_active_for_demo: boolean
  snapshot_status: string
  knowledge_score: number
  observed_at: string
  source_url?: string
  created_at: string
  updated_at: string
}

// --- Reviews ----------------------------------------------------------------

export interface ReviewListItem {
  review_id: number
  restaurant_id: number
  rating: number
  reviewed_at: string
  text: string
  language?: string
  is_representative: boolean
  topic_tags: string[]
}

export interface ReviewPage {
  items: ReviewListItem[]
  next_cursor?: string
}

export interface ReviewSummary {
  restaurant_id: number
  topic: string
  sentiment: number
  positive_ratio: number
  summary: string
  evidence_count: number
  generated_by: string
  generated_at: string
}

// --- Documents --------------------------------------------------------------

export interface DocumentListItem {
  document_id: number
  restaurant_id: number
  retrieval_scope: string
  doc_type: string
  title?: string
  content_hash: string
  version: number
  is_active: boolean
  has_embedding: boolean
  embedding_model?: string
  embedding_dimensions?: number
  snapshot_at?: string
}

export type DocumentSummary = DocumentListItem

export interface DocumentPage {
  items: DocumentListItem[]
  next_cursor?: string
}

export interface DocumentDetail extends DocumentListItem {
  content: string
  metadata: unknown
  source_record_ids: string[]
  vector_preview?: number[]
}

// --- Ingestion --------------------------------------------------------------

export interface BatchListItem {
  batch_id: number
  stage: string
  status: string
  started_at: string
  finished_at?: string
  duration_ms: number

  rows_read: number
  accepted: number
  written: number
  deduped: number
  filtered: number
  rejected: number
  unmatched: number

  documents_built?: number
  documents_embedded?: number
  documents_rejected?: number
  embedding_model?: string
  embedding_dimensions?: number
}

export interface BatchPage {
  items: BatchListItem[]
  next_cursor?: string
}

export interface RejectionItem {
  stage: string
  line_no: number
  reason: string
  source_record_id?: string
}

export interface BatchDetail extends BatchListItem {
  curation_version: string
  source_file?: string
  source_sha256?: string
  boundary_version?: string
  error_code?: string

  missing_fields: unknown
  reject_reasons?: Record<string, number>

  rejections: RejectionItem[]
  rejections_truncated: boolean
}

// --- Boundaries -------------------------------------------------------------

export interface Boundary {
  boundary_id: number
  name: string
  kind: string
  source_url?: string
  checksum?: string
  loaded_at: string
}

// --- Retrieval --------------------------------------------------------------

export type Channel = 'structured' | 'keyword' | 'vector'

export interface GeoPoint {
  longitude: number
  latitude: number
}

export interface RestaurantFilter {
  cuisines?: string[]
  price_levels?: number[]
  min_rating?: number
  neighborhood?: string
  open_now?: boolean
  borough?: string
  query_origin?: GeoPoint
  max_distance_meters?: number
}

export interface RestaurantCandidate {
  restaurant_id: number
  name: string
  address?: string
  score: number
  reasons?: string[]
  snapshot_at?: string
  rating?: number
  rating_count?: number
  price_level?: number
  cuisines?: string[]
  borough?: string
}

export interface ChannelSummary {
  channel: Channel
  ran: boolean
  weight: number
  results: number
  note?: string
}

export interface ChannelScore {
  channel: Channel
  raw_score: number
  weight: number
  normalized_score: number
  contribution: number
  reason?: string
}

export interface CandidateScore {
  restaurant_id: number
  total: number
  channels?: ChannelScore[]
}

export interface Trace {
  query?: string
  channels?: ChannelSummary[]
  candidates?: CandidateScore[]
  candidate_pool: number
  returned: number
  top_k: number
  filters?: RestaurantFilter
  embedding_model_id?: string
  query_embedding_dim?: number
  rerank_applied: boolean
  rerank_model_id?: string
  warnings?: string[]
}

export interface SearchRequest {
  query?: string
  text?: string
  filter?: RestaurantFilter
  top_k?: number
}

export interface SearchResponse {
  candidates: RestaurantCandidate[]
  trace?: Trace
}

// --- Evidence ---------------------------------------------------------------

export type DocType =
  | 'restaurant_profile'
  | 'restaurant_attributes'
  | 'restaurant_hours'
  | 'restaurant_review_summary'
  | 'restaurant_representative_reviews'

export interface Evidence {
  evidence_id: number
  restaurant_id: number
  restaurant_name?: string
  doc_type: DocType
  title?: string
  content: string
  source_record_ids?: string[]
  source: string
  snapshot_at: string
  score?: number
  topic?: string
  content_hash?: string
}

export interface EvidenceQuery {
  // Either restaurant_ids or evidence_ids is required by the service; the other
  // is not sent rather than sent empty, so the "no scope" refusal stays
  // meaningful.
  restaurant_ids?: number[]
  evidence_ids?: number[]
  query?: string
  topic?: string
  doc_types?: DocType[]
  top_k?: number
  token_budget?: number
}

export interface EvidenceTrace {
  query?: string
  scope_size: number
  topic?: string
  top_k: number
  recalled: number
  embedding_model_id?: string
  query_embedding_dim?: number
  kept: number
  dropped: number
  tokens: number
  token_budget: number
  dropped_by_reason?: Record<string, number>
  warnings?: string[]
}

// --- Evidence ---------------------------------------------------------------

// ... existing evidence types above ...

export interface EvidenceBundle {
  evidence: Evidence[]
  trace?: EvidenceTrace
}

// --- Chat / Agent console ---------------------------------------------------
//
// Everything below mirrors the /v1 conversational surface. The field names are
// the JSON tags chat-service emits and nothing else; see
// docs/platepilot-web-agent-console-task-document.md §2.1.

// ThreadState is the persisted business state of a thread. A waiting state is
// the most important thing a list row can say, which is why the console
// renders it in a distinct colour.
export type ThreadState =
  | 'idle'
  | 'awaiting_clarification'
  | 'awaiting_confirmation'
  | 'completed'
  | 'failed'

export interface CheckpointView {
  version: number
  state: ThreadState
  pending_action?: string
  missing_slots?: string[]
  evidence_ids?: number[]
  selected_restaurant_id?: number
  created_at: string
}

export interface Thread {
  thread_id: string
  user_id?: string
  title?: string
  current_state: ThreadState
  created_at: string
  updated_at: string
  last_message_at?: string
  checkpoint?: CheckpointView
}

export interface ThreadListResponse {
  conversations: Thread[]
}

export interface MessageView {
  message_id: string
  role: 'user' | 'assistant' | 'tool' | 'system'
  content: string
  evidence_ids?: number[]
  seq: number
  created_at: string
}

export interface MessageListResponse {
  messages: MessageView[]
}

// CandidateView is one row of a thread's candidate snapshot. Position is the
// ordinal a follow-up says — "第二家" — so it is carried rather than derived
// from the array index.
//
// Reasons and snapshot_at make the ranking checkable: the reasons say why this
// restaurant ranked where it did, and the snapshot date says how old the data
// behind it is. snapshot_at is absent when the store never recorded one, which
// is different from a date — a client must not render an absent field as a
// timestamp.
export interface CandidateView {
  position: number
  restaurant_id: number
  name?: string
  score?: number
  reasons?: string[]
  snapshot_at?: string
}

export interface CandidateListResponse {
  candidates: CandidateView[]
}

export interface MemoryView {
  id: string
  memory_type: string
  content: string
  source?: string
  confidence: number
  created_at: string
  updated_at: string
}

export interface MemoryListResponse {
  memories: MemoryView[]
}

export interface RunView {
  run_id: string
  thread_id: string
  trace_id?: string
  status: string
  model_provider?: string
  model_name?: string
  started_at: string
  finished_at?: string
  latency_ms?: number
  token_input?: number
  token_output?: number
  tool_call_count?: number
  error_code?: string
  // error_message is the upstream text behind a failure. error_code alone
  // classifies it; the message is what names the model and endpoint that broke.
  error_message?: string
}

export interface ToolCallView {
  call_id: string
  tool_name: string
  status: string
  latency_ms?: number
  // Only the replay endpoint carries a result summary — the live stream reports
  // that a tool ran, not what it returned.
  result_summary?: string
  arguments?: unknown
  created_at: string
}

export interface RunListResponse {
  runs: RunView[]
}

// RunDetail flattens the run's own fields next to its tool chain, so it also
// answers as a RunView.
export interface RunDetail extends RunView {
  tool_calls: ToolCallView[]
}

export interface ConfirmResponse {
  thread_id: string
  decision: 'confirm' | 'cancel'
  pending_action?: string
  state: string
  result?: unknown
  summary?: string
  replayed?: boolean
  message: string
}

export interface InterpretSoftCondition {
  text: string
  topic: string
}

// InterpretResult is the plan projection. HardFilters and SoftConditions are
// separate fields because keeping them apart is the whole point of the layer
// that produced them.
export interface InterpretResult {
  intent: string
  query?: string
  hard_filters: RestaurantFilter
  soft_conditions: InterpretSoftCondition[]
  named_restaurants: string[]
  selected_restaurant_id?: number
  missing_slots: string[]
  need_clarification: boolean
  source: 'model' | 'rules' | string
  extract_latency_ms?: number
  warnings?: string[]
}

// --- Mock inventory ---------------------------------------------------------

// ReservationSlot mirrors the backend reservation.Slot. Status derives from
// the booked count: remaining = capacity - booked.
export interface ReservationSlot {
  slot_id: string
  restaurant_id: number
  slot_date: string
  slot_time: string
  capacity: number
  booked: number
  policy_version: string
  created_at: string
}

// ReservationRecord mirrors the backend reservation.Reservation.
export interface ReservationRecord {
  reservation_id: string
  thread_id: string
  user_id: string
  restaurant_id: number
  slot_id: string
  party_size: number
  status: 'held' | 'confirmed' | 'cancelled' | 'expired' | string
  hold_expires_at?: string
  idempotency_key: string
  created_at: string
  updated_at: string
}

// InventoryView is one restaurant's bookable mock inventory as the admin
// surface serves it. Both collections are arrays, never null.
export interface InventoryView {
  restaurant_id: number
  date?: string
  slots: ReservationSlot[]
  reservations: ReservationRecord[]
}

// InventoryResetResult reports what one reset did.
export interface InventoryResetResult {
  restaurant_id: number
  date?: string
  slots_reset: number
  reservations_removed: number
}
