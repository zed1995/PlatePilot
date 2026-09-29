package port

import (
	"context"

	"github.com/zed/platepilot/shared/domain/conversation"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/memory"
	"github.com/zed/platepilot/shared/domain/run"
	"github.com/zed/platepilot/shared/domain/search"
)

// RestaurantRepository stores restaurant master data.
type RestaurantRepository interface {
	GetByID(ctx context.Context, restaurantID string) (search.RestaurantDetail, error)
	Search(ctx context.Context, query search.SearchQuery) ([]search.RestaurantCandidate, error)
	Upsert(ctx context.Context, restaurant search.RestaurantDetail) error
}

// KnowledgeRepository stores and recalls knowledge documents. Recall is always
// scoped so restaurant-level and evidence-level results never mix.
type KnowledgeRepository interface {
	FindEvidenceByRestaurant(ctx context.Context, restaurantID string) ([]evidence.Evidence, error)
	VectorSearch(ctx context.Context, scope evidence.RetrievalScope, query []float32, topK int, filter map[string]any) ([]evidence.Evidence, error)
	UpsertDocuments(ctx context.Context, docs []evidence.KnowledgeDocument) error
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
