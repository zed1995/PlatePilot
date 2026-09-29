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
