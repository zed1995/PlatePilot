package testkit

import (
	memrepo "github.com/zed/platepilot/shared/adapter/repository/memory"
)

// In-memory repository re-exports so tests can depend on testkit alone.
type (
	RestaurantRepository   = memrepo.RestaurantRepository
	KnowledgeRepository    = memrepo.KnowledgeRepository
	ConversationRepository = memrepo.ConversationRepository
	MemoryRepository       = memrepo.MemoryRepository
	RunRepository          = memrepo.RunRepository
	RestaurantStore        = memrepo.RestaurantStore
	ReviewStore            = memrepo.ReviewStore
	PipelineStore          = memrepo.PipelineStore
)

// NewRestaurantRepository returns an empty in-memory restaurant repository.
func NewRestaurantRepository() *memrepo.RestaurantRepository {
	return memrepo.NewRestaurantRepository()
}

// NewKnowledgeRepository returns an empty in-memory knowledge repository.
func NewKnowledgeRepository() *memrepo.KnowledgeRepository {
	return memrepo.NewKnowledgeRepository()
}

// NewConversationRepository returns an empty in-memory conversation repository.
func NewConversationRepository() *memrepo.ConversationRepository {
	return memrepo.NewConversationRepository()
}

// NewMemoryRepository returns an empty in-memory memory repository.
func NewMemoryRepository() *memrepo.MemoryRepository {
	return memrepo.NewMemoryRepository()
}

// NewRunRepository returns an empty in-memory run repository.
func NewRunRepository() *memrepo.RunRepository {
	return memrepo.NewRunRepository()
}

// NewRestaurantStore returns an empty in-memory write-side restaurant store.
func NewRestaurantStore() *memrepo.RestaurantStore {
	return memrepo.NewRestaurantStore()
}

// NewReviewStore returns an empty in-memory review store.
func NewReviewStore() *memrepo.ReviewStore {
	return memrepo.NewReviewStore()
}

// NewPipelineStore returns an empty in-memory pipeline audit store.
func NewPipelineStore() *memrepo.PipelineStore {
	return memrepo.NewPipelineStore()
}
