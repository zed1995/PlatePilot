package testkit

import (
	"fmt"
	"time"

	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/memory"
	"github.com/zed1995/platepilot/shared/domain/restaurant"
	"github.com/zed1995/platepilot/shared/domain/review"
	"github.com/zed1995/platepilot/shared/domain/run"
	"github.com/zed1995/platepilot/shared/domain/search"
)

// FixedSnapshotAt is the single observed_at used across fixtures, matching the
// 2021-09 snapshot of the Google Local dataset.
var FixedSnapshotAt = time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)

// SampleRestaurant returns a deterministic restaurant fixture.
func SampleRestaurant(id int64, name string) search.RestaurantDetail {
	price := 2
	rating := 4.5
	return search.RestaurantDetail{
		RestaurantID: id,
		Name:         name,
		Address:      "7 Carmine St, New York, NY",
		Cuisines:     []string{"pizza", "italian"},
		PriceLevel:   &price,
		Rating:       &rating,
		Source:       "google_local_2021",
		SnapshotAt:   FixedSnapshotAt,
		Attributes:   map[string]string{"outdoor_seating": "true", "open_now": "true"},
	}
}

// SampleKnowledgeDocument returns a deterministic knowledge document fixture.
func SampleKnowledgeDocument(id, restaurantID int64, scope evidence.RetrievalScope, content string) evidence.KnowledgeDocument {
	return evidence.KnowledgeDocument{
		DocumentID:   id,
		RestaurantID: restaurantID,
		Scope:        scope,
		DocType:      evidence.DocTypeRestaurantReviewSummary,
		Title:        "Service experience",
		Content:      content,
		Metadata:     map[string]any{"source": "google_local_2021"},
		SnapshotAt:   FixedSnapshotAt,
		Version:      1,
		IsActive:     true,
	}
}

// SampleConversation returns a deterministic conversation fixture.
func SampleConversation(threadID, userID string) conversation.Conversation {
	return conversation.Conversation{
		ThreadID:      threadID,
		UserID:        userID,
		Title:         "Find a quiet pizza place",
		CurrentState:  conversation.StateIdle,
		CreatedAt:     FixedSnapshotAt,
		UpdatedAt:     FixedSnapshotAt,
		LastMessageAt: FixedSnapshotAt,
	}
}

// SampleCheckpoint returns a deterministic checkpoint fixture.
func SampleCheckpoint(threadID string, version int64) conversation.Checkpoint {
	return conversation.Checkpoint{
		ThreadID:     threadID,
		Version:      version,
		State:        conversation.StateAwaitingClarification,
		MissingSlots: []string{"neighborhood"},
		CreatedAt:    FixedSnapshotAt,
	}
}

// SampleRun returns a deterministic agent run fixture.
func SampleRun(runID, traceID string) run.AgentRun {
	return run.AgentRun{
		TraceID:   traceID,
		ThreadID:  "thread-1",
		RunID:     runID,
		Status:    run.StatusRunning,
		StartedAt: FixedSnapshotAt,
	}
}

// SampleMemory returns a deterministic memory fixture.
func SampleMemory(userID, content string) memory.Memory {
	return memory.Memory{
		UserID:     userID,
		Type:       memory.MemoryTypePreference,
		Content:    content,
		Source:     "user_explicit",
		Confidence: 1,
		CreatedAt:  FixedSnapshotAt,
		UpdatedAt:  FixedSnapshotAt,
	}
}

// SampleCuratedRestaurant returns a deterministic curated restaurant fixture.
func SampleCuratedRestaurant(id int64, sourceRecordID, name string) restaurant.Restaurant {
	price := 2
	avg := 4.5
	return restaurant.Restaurant{
		ID:             id,
		Source:         restaurant.SourceGoogleLocal2021,
		SourceRecordID: sourceRecordID,
		Name:           name,
		Address:        "7 Carmine St, New York, NY",
		BoroughGuess:   "manhattan",
		Location:       &restaurant.GeoPoint{Longitude: -74.002, Latitude: 40.730},
		Categories:     []string{"Pizza restaurant", "Restaurant"},
		CuisineTags:    []string{"pizza", "italian"},
		Description:    "Neighbourhood pizza slice shop",
		Price:          restaurant.Price{Raw: "$$", Level: &price},
		Rating:         restaurant.Rating{SourceAvg: &avg},
		Attributes: restaurant.Attributes{
			TriStates:      map[string]string{"outdoor_seating": restaurant.TriStateTrue},
			AtmosphereTags: []string{"casual"},
		},
		SnapshotStatus: restaurant.StatusOpen,
		ObservedAt:     FixedSnapshotAt,
		CreatedAt:      FixedSnapshotAt,
		UpdatedAt:      FixedSnapshotAt,
	}
}

// SampleReview returns a deterministic curated review fixture.
func SampleReview(id, restaurantID int64, text string) review.Review {
	return review.Review{
		ID:               id,
		RestaurantID:     restaurantID,
		Rating:           5,
		ReviewedAt:       FixedSnapshotAt,
		Text:             text,
		Language:         "en",
		TextHash:         fmt.Sprintf("sha256:%d", id),
		SourceObservedAt: FixedSnapshotAt,
	}
}
