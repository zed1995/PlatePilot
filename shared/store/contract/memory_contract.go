package contract

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	domainmemory "github.com/zed1995/platepilot/shared/domain/memory"
	"github.com/zed1995/platepilot/shared/store"
)

// runMemoryRepositoryContract pins the user-memory lifecycle for both
// adapters: upsert with generated id, updated-at-desc ordering, and soft
// delete that hides rows from List without destroying them.
func runMemoryRepositoryContract(t *testing.T, memories store.MemoryRepository) {
	ctx := context.Background()
	const userID = "mem-user-1"

	t.Run("upsert_assigns_id_and_preserves_created_at", func(t *testing.T) {
		embedding := make([]float32, 1024)
		embedding[0] = 0.5
		embedding[1023] = 0.25
		record := domainmemory.Memory{
			UserID:     userID,
			Type:       domainmemory.MemoryTypeConstraint,
			Content:    "不吃辣",
			Source:     "user_explicit",
			Confidence: 0.9,
			Embedding:  embedding,
		}
		if err := memories.Upsert(ctx, record); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := memories.List(ctx, userID)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		saved := got[0]
		if saved.ID == "" {
			t.Fatal("ID should be assigned")
		}
		if saved.CreatedAt.IsZero() || saved.UpdatedAt.IsZero() {
			t.Fatal("timestamps should be assigned")
		}
		if saved.Confidence != 0.9 || saved.Source != "user_explicit" || saved.Type != domainmemory.MemoryTypeConstraint {
			t.Fatalf("scalar fields lost: %+v", saved)
		}
		if len(saved.Embedding) != 1024 || saved.Embedding[0] != 0.5 || saved.Embedding[1023] != 0.25 {
			t.Fatalf("embedding round trip failed: len=%d head=%v tail=%v",
				len(saved.Embedding), firstFloats(saved.Embedding), lastFloats(saved.Embedding))
		}

		// An update keeps the original identity and creation time.
		createdAt := saved.CreatedAt
		saved.Content = "不吃辣，也不吃香菜"
		saved.Confidence = 0.95
		if err := memories.Upsert(ctx, saved); err != nil {
			t.Fatalf("Upsert update: %v", err)
		}
		got, err = memories.List(ctx, userID)
		if err != nil {
			t.Fatalf("List after update: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1 (update must not insert)", len(got))
		}
		if got[0].Content != "不吃辣，也不吃香菜" || got[0].Confidence != 0.95 {
			t.Fatalf("update not persisted: %+v", got[0])
		}
		if !got[0].CreatedAt.Equal(createdAt) {
			t.Fatalf("created_at changed: got %s want %s", got[0].CreatedAt, createdAt)
		}
	})

	t.Run("list_orders_by_updated_at_desc_and_filters_deleted", func(t *testing.T) {
		user := "mem-user-order"
		// Sequential inserts stamp increasing updated_at.
		var ids [3]string
		contents := [3]string{"oldest", "middle", "newest"}
		for i := range ids {
			m := domainmemory.Memory{UserID: user, Type: domainmemory.MemoryTypeFact, Content: contents[i], Confidence: 0.5}
			if err := memories.Upsert(ctx, m); err != nil {
				t.Fatalf("Upsert %d: %v", i, err)
			}
			listed, err := memories.List(ctx, user)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			ids[i] = listed[0].ID
		}
		got, err := memories.List(ctx, user)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 3 || got[0].ID != ids[2] || got[1].ID != ids[1] || got[2].ID != ids[0] {
			t.Fatalf("insert order = %+v, want newest first %v", idOrder(got), ids)
		}

		// Touching the oldest row must move it to the head (updated_at desc,
		// not created_at desc). The sleep clears the stores' timestamp
		// resolution (PostgreSQL now() is microsecond granular).
		time.Sleep(2 * time.Millisecond)
		oldest := got[2]
		oldest.Content = "refreshed oldest"
		if err := memories.Upsert(ctx, oldest); err != nil {
			t.Fatalf("Upsert refresh: %v", err)
		}
		got, err = memories.List(ctx, user)
		if err != nil {
			t.Fatalf("List after refresh: %v", err)
		}
		if len(got) != 3 || got[0].ID != oldest.ID {
			t.Fatalf("refreshed row should be first: %+v", idOrder(got))
		}

		// Soft delete hides the row from List.
		if err := memories.Delete(ctx, user, ids[1]); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		got, err = memories.List(ctx, user)
		if err != nil {
			t.Fatalf("List after delete: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("deleted row still listed: %+v", idOrder(got))
		}
		for _, m := range got {
			if m.ID == ids[1] {
				t.Fatalf("deleted memory returned by List: %+v", m)
			}
		}

		// Deleting again is a not_found (the row is already gone from the
		// user-visible set), and so is deleting another user's memory.
		if err := memories.Delete(ctx, user, ids[1]); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("double delete: want ErrNotFound, got %v", err)
		}
		if err := memories.Delete(ctx, "mem-user-other", oldest.ID); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("cross-user delete: want ErrNotFound, got %v", err)
		}
		// The other user's listing is unaffected.
		if _, err := memories.List(ctx, "mem-user-other"); err != nil {
			t.Fatalf("empty List should not error: %v", err)
		}
	})

	t.Run("requires_user_id", func(t *testing.T) {
		if err := memories.Upsert(ctx, domainmemory.Memory{Content: "x"}); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("Upsert without user_id: want invalid_argument, got %v", err)
		}
		if _, err := memories.List(ctx, ""); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("List without user_id: want invalid_argument, got %v", err)
		}
	})

	// Search is the read path injection uses once a user has more memories than
	// the prompt window can hold. Its whole value is that it answers a
	// different question from List, so the cases below are about *which* rows
	// come back, not about whether the query ran.
	t.Run("search_matches_content_case_insensitively", func(t *testing.T) {
		user := "mem-user-search"
		for _, m := range []domainmemory.Memory{
			{UserID: user, Type: domainmemory.MemoryTypePreference, Content: "Loves Quiet Italian Places", Confidence: 0.6},
			{UserID: user, Type: domainmemory.MemoryTypePreference, Content: "喜欢安静的餐厅", Confidence: 0.6},
			{UserID: user, Type: domainmemory.MemoryTypePreference, Content: "只看 Brooklyn", Confidence: 0.6},
		} {
			if err := memories.Upsert(ctx, m); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
		}

		// Case is the user's, not the schema's: a search must not depend on
		// whether the memory was written in one and the query asked in the
		// other.
		got, err := memories.Search(ctx, user, "quiet", 10)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 1 || !strings.Contains(got[0].Content, "Quiet") {
			t.Fatalf("case-insensitive search = %+v, want the Quiet memory", contentsOf(got))
		}

		// Chinese has no word boundaries, so a term is not a word: it is a
		// span, and the match is a substring test. This is the case a
		// tokeniser-based implementation would silently fail.
		got, err = memories.Search(ctx, user, "安静的餐厅", 10)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 1 || got[0].Content != "喜欢安静的餐厅" {
			t.Fatalf("substring search = %+v, want the Chinese memory", contentsOf(got))
		}
	})

	t.Run("search_ranks_by_how_many_terms_hit", func(t *testing.T) {
		user := "mem-user-search-rank"
		// The older row answers both halves of the question; the newer one
		// answers a single half. A search that "ranks" by updated_at returns
		// the newer row first and still looks reasonable in a one-row
		// fixture, so the newer row is written last on purpose.
		for _, content := range []string{"布鲁克林的意大利菜", "只看布鲁克林"} {
			if err := memories.Upsert(ctx, domainmemory.Memory{
				UserID: user, Type: domainmemory.MemoryTypePreference,
				Content: content, Confidence: 0.6,
			}); err != nil {
				t.Fatalf("Upsert %q: %v", content, err)
			}
			// Clear the stores' timestamp resolution so the two rows differ
			// in updated_at at all (PostgreSQL now() is microsecond granular).
			time.Sleep(2 * time.Millisecond)
		}

		got, err := memories.Search(ctx, user, "布鲁克林 意大利菜", 10)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("multi-term search = %+v, want both memories", contentsOf(got))
		}
		if got[0].Content != "布鲁克林的意大利菜" {
			t.Fatalf("the row matching both terms ranked second: %+v", contentsOf(got))
		}

		// Two runes is below the floor a term has to clear, so a query made
		// only of short fields has nothing left to match on. Both of these
		// appear verbatim in the rows above — returning nothing is the
		// point, and it must not degrade into "no terms, so match everything".
		short, err := memories.Search(ctx, user, "布鲁 只看", 10)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(short) != 0 {
			t.Fatalf("query of too-short terms matched %+v, want nothing", contentsOf(short))
		}
	})

	t.Run("search_excludes_deleted_and_other_users", func(t *testing.T) {
		user := "mem-user-search-scope"
		if err := memories.Upsert(ctx, domainmemory.Memory{
			UserID: user, Type: domainmemory.MemoryTypeFact, Content: "住在上西区", Confidence: 0.5,
		}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := memories.Upsert(ctx, domainmemory.Memory{
			UserID: "mem-user-search-neighbour", Type: domainmemory.MemoryTypeFact,
			Content: "住在上西区", Confidence: 0.5,
		}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}

		rows, err := memories.List(ctx, user)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("list = %d, want 1", len(rows))
		}

		got, err := memories.Search(ctx, user, "上西区", 10)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 1 || got[0].UserID != user {
			t.Fatalf("search leaked across users: %+v", got)
		}

		// A soft delete removes the row from search on the next call, without
		// waiting for anything to expire. Injection reads through this path, so
		// a memory the user deleted that stayed searchable would keep steering
		// answers.
		if err := memories.Delete(ctx, user, rows[0].ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		got, err = memories.Search(ctx, user, "上西区", 10)
		if err != nil {
			t.Fatalf("Search after delete: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("deleted memory still searchable: %+v", contentsOf(got))
		}
	})

	t.Run("search_honours_limit_and_refuses_a_useless_query", func(t *testing.T) {
		user := "mem-user-search-limit"
		for i := 0; i < 3; i++ {
			if err := memories.Upsert(ctx, domainmemory.Memory{
				UserID: user, Type: domainmemory.MemoryTypePreference,
				Content: fmt.Sprintf("餐厅偏好 %d", i), Confidence: 0.5,
			}); err != nil {
				t.Fatalf("Upsert %d: %v", i, err)
			}
		}

		got, err := memories.Search(ctx, user, "餐厅偏好", 2)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("limit ignored: got %d rows, want 2", len(got))
		}

		// "Nothing to search for" and "the search matched everything" are
		// different answers. Returning the user's whole memory set for an empty
		// query would be the second while looking like the first, and every
		// caller of this path is a caller that wanted the first.
		for _, query := range []string{"", "   ", "的"} {
			got, err := memories.Search(ctx, user, query, 10)
			if err != nil {
				t.Fatalf("Search(%q): %v", query, err)
			}
			if len(got) != 0 {
				t.Fatalf("Search(%q) = %+v, want nothing", query, contentsOf(got))
			}
		}

		if _, err := memories.Search(ctx, "", "餐厅", 10); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("Search without user_id: want invalid_argument, got %v", err)
		}
		if _, err := memories.Search(ctx, user, "餐厅", 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("Search with a zero limit: want invalid_argument, got %v", err)
		}
	})
}

func idOrder(records []domainmemory.Memory) []string {
	out := make([]string, len(records))
	for i, m := range records {
		out[i] = m.ID
	}
	return out
}

// contentsOf renders a result set as the contents that came back, in order.
//
// Failure messages quote it instead of the full records because the thing
// being asserted in every search case is *which* memories matched and in what
// order; printing ids and timestamps buries that.
func contentsOf(records []domainmemory.Memory) []string {
	out := make([]string, len(records))
	for i, m := range records {
		out[i] = m.Content
	}
	return out
}

func firstFloats(vec []float32) []float32 {
	if len(vec) < 3 {
		return vec
	}
	return vec[:3]
}

func lastFloats(vec []float32) []float32 {
	if len(vec) < 3 {
		return vec
	}
	return vec[len(vec)-3:]
}
