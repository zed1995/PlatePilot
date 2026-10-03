package conversation

import "time"

// Candidate is one restaurant from the most recent search a thread ran, kept
// so a follow-up can refer to it by position.
//
// The list is a position-indexed snapshot rather than accumulated history.
// "第二家" is only meaningful against one ordered list, and a store that
// appended every search's results would make the ordinal ambiguous the moment a
// second search ran. Replacing the set wholesale — and only when a turn
// actually produced candidates — keeps the position semantics stable.
type Candidate struct {
	ThreadID     string    `json:"thread_id"`
	Position     int       `json:"position"`
	RestaurantID int64     `json:"restaurant_id"`
	Name         string    `json:"name"`
	Score        float64   `json:"score"`
	Reasons      []string  `json:"reasons,omitempty"`
	SnapshotAt   time.Time `json:"snapshot_at"`
	CreatedAt    time.Time `json:"created_at"`
}
