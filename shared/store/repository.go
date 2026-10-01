package store

import (
	"context"

	"github.com/zed/platepilot/shared/domain/conversation"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/memory"
	"github.com/zed/platepilot/shared/domain/run"
	"github.com/zed/platepilot/shared/domain/search"
)

// RestaurantRepository reads restaurant master data for the retrieval layer.
//
// It has no write methods on purpose. Writing goes through RestaurantStore,
// which is a different interface on a different side of the two-service split:
// the pipeline writes, the chat service reads, and neither service implements
// the other's port.
type RestaurantRepository interface {
	// GetByID returns one restaurant, or errs.ErrNotFound.
	GetByID(ctx context.Context, restaurantID int64) (search.RestaurantDetail, error)
	// Search applies every hard filter in query.Filter and returns candidates
	// ordered by score. Text is the keyword channel's input and is not
	// interpreted here; an empty Text with a populated Filter is a valid query
	// that filters without matching text.
	Search(ctx context.Context, query search.SearchQuery) ([]search.RestaurantCandidate, error)
	// MatchByText finds restaurants whose name or address resembles text. It
	// exists separately from Search because the two answer different questions:
	// Search enforces conditions, MatchByText proposes candidates. Fusing them
	// without a separate step would either make a fuzzy name match obey hard
	// filters it cannot see, or make the filters optional.
	//
	// Scores are a similarity in [0,1] so a caller can weight them against
	// other channels without re-scaling.
	MatchByText(ctx context.Context, text string, limit int) ([]search.RestaurantCandidate, error)
}

// KnowledgeRepository recalls knowledge documents for the retrieval layer.
//
// Recall is always scoped, and an evidence recall is always scoped to specific
// restaurants. Both constraints exist because the failure they prevent is
// invisible: a chunk from another restaurant is a confidently wrong citation,
// not an error.
type KnowledgeRepository interface {
	// FindEvidenceByRestaurant returns every active evidence document for one
	// restaurant. An empty topic returns all topics.
	FindEvidenceByRestaurant(ctx context.Context, restaurantID int64, topic string) ([]evidence.Evidence, error)
	// VectorSearch ranks active documents in one scope by cosine distance.
	//
	// Scope is required, not defaulted: a search that could return both a
	// restaurant profile and an evidence chunk would let a profile outrank the
	// quote a caller meant to retrieve, with nothing downstream able to tell
	// them apart.
	VectorSearch(ctx context.Context, req VectorSearchRequest) ([]ScoredDocument, error)
	// RecallEvidence is the evidence-scope read the chat layer calls. It exists
	// as its own method rather than as a VectorSearch call site because the rule
	// it enforces -- a non-empty restaurant set, or nothing -- belongs with the
	// code that issues the query. Enforcing it at each call site means the next
	// caller has to remember it, and a citation from another restaurant is not
	// an error anyone would notice.
	RecallEvidence(ctx context.Context, req EvidenceRequest) ([]evidence.Evidence, error)
}

// EvidenceRequest is one restaurant-bounded evidence recall.
//
// Query is the already-embedded question; an empty Query with a populated
// RestaurantIDs is the "tell me everything about this place" read, which is a
// legitimate question and returns the restaurant's active documents in a stable
// order rather than an error.
type EvidenceRequest struct {
	// RestaurantIDs is required and must be non-empty. There is no "all
	// restaurants" form: see KnowledgeRepository.RecallEvidence.
	RestaurantIDs []int64
	// Query is the embedded question, or nil for the unordered read.
	Query []float32
	// Topic narrows review summaries to one review topic. It is a secondary
	// filter, not a replacement for the vector: two restaurants' reviews on the
	// same topic still differ enough in wording that ordering them needs
	// similarity, not equality.
	Topic string
	// DocTypes, when non-empty, restricts results to these document kinds.
	DocTypes []evidence.DocType
	// TopK bounds the result count.
	TopK int
}

// VectorSearchRequest is one scoped vector recall.
//
// The filter is an explicit struct rather than a map on purpose. A map lets a
// caller pass a constraint the store silently ignores — a restaurant id written
// as a string, a borough in the wrong case — and the only symptom is a wrong
// answer, which is exactly the failure this layer exists to prevent.
type VectorSearchRequest struct {
	// Scope selects restaurant-level or evidence-level documents.
	Scope evidence.RetrievalScope
	// Query is the embedded question. It must be exactly as long as the
	// column, which is checked by the store before the query is issued.
	Query []float32
	// TopK is the maximum number of rows to return.
	TopK int
	// Borough, when set, selects the borough-partitioned index. It is the
	// difference between an ordered index scan and a filtered table scan, so
	// callers that know the borough must pass it.
	Borough string
	// RestaurantIDs, when non-empty, restricts results to these restaurants.
	// An empty slice means "no restriction" and must not be rendered as a
	// restriction that matches nothing.
	RestaurantIDs []int64
	// DocTypes, when non-empty, restricts results to these document types.
	DocTypes []evidence.DocType
	// Topic restricts review-summary documents to one review topic.
	Topic string
}

// ConversationRepository persists thread metadata and recoverable checkpoints.
type ConversationRepository interface {
	Get(ctx context.Context, threadID string) (conversation.Conversation, error)
	Upsert(ctx context.Context, conv conversation.Conversation) error
	SaveCheckpoint(ctx context.Context, checkpoint conversation.Checkpoint) error
	LoadCheckpoint(ctx context.Context, threadID string) (conversation.Checkpoint, error)
}

// MemoryRepository manages user-controlled long-term memories.
type MemoryRepository interface {
	List(ctx context.Context, userID string) ([]memory.Memory, error)
	Upsert(ctx context.Context, mem memory.Memory) error
	Delete(ctx context.Context, userID, memoryID string) error
}

// RunRepository records agent runs and tool call audits.
type RunRepository interface {
	Start(ctx context.Context, agentRun run.AgentRun) error
	Finish(ctx context.Context, agentRun run.AgentRun) error
	RecordToolCall(ctx context.Context, call run.ToolCallRecord) error
}
