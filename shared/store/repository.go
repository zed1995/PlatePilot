package store

import (
	"context"
	"time"

	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/memory"
	"github.com/zed1995/platepilot/shared/domain/reservation"
	"github.com/zed1995/platepilot/shared/domain/run"
	"github.com/zed1995/platepilot/shared/domain/search"
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

// ConversationRepository persists thread metadata, recoverable checkpoints,
// and the per-thread message transcript.
type ConversationRepository interface {
	Get(ctx context.Context, threadID string) (conversation.Conversation, error)
	Upsert(ctx context.Context, conv conversation.Conversation) error
	SaveCheckpoint(ctx context.Context, checkpoint conversation.Checkpoint) error
	LoadCheckpoint(ctx context.Context, threadID string) (conversation.Checkpoint, error)
	// AppendMessage stores one message and assigns its per-thread seq.
	AppendMessage(ctx context.Context, msg conversation.Message) error
	// ListMessages returns up to limit messages immediately before beforeID
	// (exclusive; the newest messages when beforeID is empty), in ascending
	// chronological order. A non-positive limit means no limit.
	ListMessages(ctx context.Context, threadID string, limit int, beforeID string) ([]conversation.Message, error)

	// ReplaceCandidates swaps a thread's candidate snapshot for the given set.
	//
	// It replaces rather than appends because position is the whole point: a
	// follow-up says "第二家", and that ordinal is only well defined against one
	// ordered list. Appending would leave two lists and an ordinal that means
	// different restaurants depending on which turn it was spoken in.
	//
	// An empty set is a real request and clears the snapshot, which is what a
	// turn that produced no candidates needs. A turn that produced none but did
	// not search simply never calls this.
	ReplaceCandidates(ctx context.Context, threadID string, candidates []conversation.Candidate) error
	// ListCandidates returns a thread's current candidates in position order.
	ListCandidates(ctx context.Context, threadID string) ([]conversation.Candidate, error)
}

// MemoryRepository manages user-controlled long-term memories.
type MemoryRepository interface {
	List(ctx context.Context, userID string) ([]memory.Memory, error)
	Upsert(ctx context.Context, mem memory.Memory) error
	Delete(ctx context.Context, userID, memoryID string) error
}

// RunRepository records agent runs and tool call audits.
//
// M4 wrote these rows and never read them; M5 adds the read side so a
// recommendation's tool chain can be replayed afterwards. The reads return the
// same rows the writer stored — arguments on a tool call are the redacted
// digest the audit assembled, never the raw model payload — because a read
// endpoint that widened what it returns would undo the redaction at the point
// where it is easiest to overlook.
type RunRepository interface {
	Start(ctx context.Context, agentRun run.AgentRun) error
	Finish(ctx context.Context, agentRun run.AgentRun) error
	RecordToolCall(ctx context.Context, call run.ToolCallRecord) error

	// GetRun returns one run by id, or errs.ErrNotFound.
	GetRun(ctx context.Context, runID string) (run.AgentRun, error)
	// ListRuns returns up to limit runs for a thread, newest first. beforeID is
	// an exclusive cursor in the same style as the message paging API: when
	// non-empty, only runs started strictly before that run are returned.
	ListRuns(ctx context.Context, threadID string, limit int, beforeID string) ([]run.AgentRun, error)
	// ListToolCalls returns one run's tool calls in invocation order. An
	// unknown run is not an error and yields an empty slice: a run that called
	// no tools and a run id that does not exist are both "no rows", and the
	// caller that needs to tell them apart reads the run first.
	ListToolCalls(ctx context.Context, runID string) ([]run.ToolCallRecord, error)
}

// ReservationRepository persists mock reservation slots, holds, and bookings.
//
// The capability is deliberately Mock-sized: one table of slots per restaurant
// per date, capacity tracked as a counter, and a unique idempotency key that
// makes a repeated confirmation idempotent. It is not a booking system and the
// port says so by having no cancel or reschedule method.
type ReservationRepository interface {
	// EnsureSlots creates any missing slots and returns nothing when they all
	// exist. The mock inventory is generated from a template rather than
	// imported, so the first read of a day materialises that day's slots.
	EnsureSlots(ctx context.Context, slots []reservation.Slot) error
	// ListSlots returns one restaurant's slots for one date, in time order.
	ListSlots(ctx context.Context, restaurantID int64, date string) ([]reservation.Slot, error)
	// GetSlot returns one slot by id, or errs.ErrNotFound.
	GetSlot(ctx context.Context, slotID string) (reservation.Slot, error)
	// HoldSlot atomically reserves partySize seats when the slot has room, and
	// returns errs.ErrReservationUnavailable otherwise. The check and the
	// increment must be one operation: a read-then-write would let two
	// concurrent holds both see the last seat.
	HoldSlot(ctx context.Context, slotID string, partySize int) (reservation.Slot, error)
	// ReleaseSlot returns seats to a slot after a hold is cancelled or expires.
	ReleaseSlot(ctx context.Context, slotID string, partySize int) error
	// SaveReservation inserts or updates a reservation.
	SaveReservation(ctx context.Context, res reservation.Reservation) error
	// GetReservation returns one reservation by id, or errs.ErrNotFound.
	GetReservation(ctx context.Context, reservationID string) (reservation.Reservation, error)
	// GetByIdempotencyKey returns the reservation a key already produced, or
	// errs.ErrNotFound. It is the read half of the idempotent confirmation.
	GetByIdempotencyKey(ctx context.Context, key string) (reservation.Reservation, error)
	// ReleaseExpiredHolds expires held reservations whose TTL passed and
	// returns the seats to their slots. It returns how many it expired.
	ReleaseExpiredHolds(ctx context.Context, now time.Time) (int, error)
}
