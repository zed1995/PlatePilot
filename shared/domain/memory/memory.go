// Package memory defines long-term user memory DTOs.
package memory

import "time"

// MemoryType classifies a stored user memory.
type MemoryType string

const (
	MemoryTypePreference MemoryType = "preference"
	MemoryTypeConstraint MemoryType = "constraint"
	MemoryTypeFact       MemoryType = "fact"
)

// Memory is a single, user-managed long-term memory. Memories are only written
// on explicit user request and are always viewable and deletable.
type Memory struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Type       MemoryType `json:"memory_type"`
	Content    string     `json:"content"`
	Source     string     `json:"source,omitempty"`
	Confidence float64    `json:"confidence"`
	Embedding  []float32  `json:"embedding,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
}
