import type {
  BatchDetail,
  BatchPage,
  Boundary,
  DocumentDetail,
  DocumentPage,
  DocumentSummary,
  EvidenceBundle,
  EvidenceQuery,
  Overview,
  RestaurantDetail,
  RestaurantPage,
  ReviewPage,
  ReviewSummary,
  SearchRequest,
  SearchResponse,
} from './types'

// Empty by default: in development Vite proxies /admin and /v1 to the local
// service. An absolute origin may be set when the console runs elsewhere.
const base = import.meta.env.VITE_API_BASE ?? ''

// ApiError carries the backend error envelope. The code is what UI logic
// branches on (e.g. not_found); the message is for the operator.
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly requestId?: string

  constructor(status: number, code: string, message: string, requestId?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.requestId = requestId
  }
}

interface ErrorEnvelope {
  error?: {
    code?: string
    message?: string
    request_id?: string
  }
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const init: RequestInit = { method }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }

  const response = await fetch(base + path, init)
  const data = (await response.json().catch(() => null)) as (T & ErrorEnvelope) | null

  if (!response.ok) {
    const envelope = data?.error
    throw new ApiError(
      response.status,
      envelope?.code ?? 'unknown',
      envelope?.message ?? `request failed with status ${response.status}`,
      envelope?.request_id,
    )
  }

  return data as T
}

const get = <T>(path: string): Promise<T> => request<T>('GET', path)
const post = <T>(path: string, body: unknown): Promise<T> =>
  request<T>('POST', path, body)

// buildQuery renders only set parameters so unset never becomes an empty
// filter.
export function buildQuery<T extends object>(params: T): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== '') {
      search.set(key, String(value))
    }
  }
  const encoded = search.toString()
  return encoded ? `?${encoded}` : ''
}

export interface RestaurantsParams {
  cursor?: string
  limit?: number
  borough?: string
  cuisine?: string
  active?: boolean
  q?: string
}

export interface ReviewsParams {
  cursor?: string
  limit?: number
}

export interface DocumentsParams {
  cursor?: string
  limit?: number
  restaurant_id?: number
  scope?: string
  doc_type?: string
  is_active?: boolean
  has_embedding?: boolean
}

export interface BatchesParams {
  cursor?: string
  limit?: number
  stage?: string
}

// adminApi is the single surface pages use.
export const adminApi = {
  overview: () => get<Overview>('/admin/v1/overview'),

  restaurants: (params: RestaurantsParams) =>
    get<RestaurantPage>(`/admin/v1/restaurants${buildQuery(params)}`),
  restaurant: (id: number) =>
    get<RestaurantDetail>(`/admin/v1/restaurants/${id}`),

  reviews: (id: number, params: ReviewsParams) =>
    get<ReviewPage>(
      `/admin/v1/restaurants/${id}/reviews${buildQuery(params)}`,
    ),
  summaries: (id: number) =>
    get<ReviewSummary[]>(`/admin/v1/restaurants/${id}/summaries`),
  restaurantDocuments: (id: number) =>
    get<DocumentSummary[]>(`/admin/v1/restaurants/${id}/documents`),

  documents: (params: DocumentsParams) =>
    get<DocumentPage>(`/admin/v1/documents${buildQuery(params)}`),
  document: (id: number, vectorPreview = false) =>
    get<DocumentDetail>(
      `/admin/v1/documents/${id}${buildQuery({
        vector_preview: vectorPreview,
      })}`,
    ),

  batches: (params: BatchesParams) =>
    get<BatchPage>(`/admin/v1/batches${buildQuery(params)}`),
  batch: (id: number) => get<BatchDetail>(`/admin/v1/batches/${id}`),

  boundaries: () => get<Boundary[]>('/admin/v1/boundaries'),

  debugSearch: (body: SearchRequest) =>
    post<SearchResponse>('/admin/v1/debug/search', body),
  debugEvidence: (body: EvidenceQuery) =>
    post<EvidenceBundle>('/admin/v1/debug/evidence', body),
}
