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
  restaurant_ids: number[]
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

export interface EvidenceBundle {
  evidence: Evidence[]
  trace?: EvidenceTrace
}
