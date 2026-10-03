package memorywrite_test

import (
	"context"
	"strings"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/errs"
	domainmemory "github.com/zed1995/platepilot/shared/domain/memory"
	"github.com/zed1995/platepilot/shared/store"
	"github.com/zed1995/platepilot/shared/testkit"

	"github.com/zed1995/platepilot/chat-service/internal/memorywrite"
)

const fixtureUser = "user-1"

// requestWith builds a save request for a user who said message.
func requestWith(message, content string, memType domainmemory.MemoryType) (context.Context, memorywrite.Request) {
	ctx := memorywrite.WithTurn(context.Background(), memorywrite.Turn{
		UserID:  fixtureUser,
		Message: message,
	})
	return ctx, memorywrite.Request{
		Content:        content,
		Type:           memType,
		QuotedUserText: message,
	}
}

func newService(t *testing.T, repo store.MemoryRepository) *memorywrite.Service {
	t.Helper()
	svc, err := memorywrite.NewService(repo)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// ---- the guard ------------------------------------------------------------

// The guard is the whole product rule: a memory may only be created when the
// user asked for it, and "asked for it" is a substring check rather than a
// prompt sentence. A model that decided on its own that a preference was worth
// keeping has nothing to quote.
func TestAFabricatedRequestIsRefusedAndNothingIsWritten(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewMemoryRepository()
	svc := newService(t, repo)

	// The user said something that mentions no preference at all; the model
	// invented one.
	turnCtx := memorywrite.WithTurn(ctx, memorywrite.Turn{
		UserID:  fixtureUser,
		Message: "帮我找一家安静的意大利餐厅",
	})
	_, err := svc.Save(turnCtx, memorywrite.Request{
		Content:        "喜欢安静的环境",
		Type:           domainmemory.MemoryTypePreference,
		QuotedUserText: "我喜欢安静的环境",
	})
	if err == nil {
		t.Fatal("expected the fabricated quote to be refused")
	}
	if code := errs.CodeOf(err); code != errs.CodeMemoryWriteNotRequested {
		t.Fatalf("code = %q, want %q", code, errs.CodeMemoryWriteNotRequested)
	}
	assertEmpty(t, ctx, repo)
}

// Whitespace and case are presentation, not meaning: a quote with a trailing
// space is describing the same request. Everything else is kept, so a quote that
// paraphrases is still refused.
func TestTheGuardNormalizesWhitespaceAndCaseButNotWording(t *testing.T) {
	cases := []struct {
		name    string
		message string
		quote   string
		wantOK  bool
	}{
		{
			name:    "verbatim",
			message: "记住我不吃辣",
			quote:   "我不吃辣",
			wantOK:  true,
		},
		{
			name:    "trailing and leading whitespace in the quote",
			message: "记住我不吃辣",
			quote:   "  我不吃辣 ",
			wantOK:  true,
		},
		{
			name:    "repeated internal whitespace",
			message: "记住 我  不吃辣",
			quote:   "记住 我 不吃辣",
			wantOK:  true,
		},
		{
			name:    "different case in a latin fragment",
			message: "Remember I like Pizza",
			quote:   "remember i like pizza",
			wantOK:  true,
		},
		{
			name:    "wording changed",
			message: "记住我不吃辣",
			quote:   "我不吃辣的",
			wantOK:  false,
		},
		{
			name:    "invented outright",
			message: "帮我找一家意大利餐厅",
			quote:   "我喜欢意大利菜",
			wantOK:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := testkit.NewMemoryRepository()
			svc := newService(t, repo)

			turnCtx := memorywrite.WithTurn(ctx, memorywrite.Turn{
				UserID:  fixtureUser,
				Message: tc.message,
			})
			_, err := svc.Save(turnCtx, memorywrite.Request{
				Content:        "不吃辣",
				Type:           domainmemory.MemoryTypeConstraint,
				QuotedUserText: tc.quote,
			})
			if tc.wantOK && err != nil {
				t.Fatalf("a legitimate quote was refused: %v", err)
			}
			if !tc.wantOK {
				if err == nil {
					t.Fatal("expected a refusal")
				}
				if code := errs.CodeOf(err); code != errs.CodeMemoryWriteNotRequested {
					t.Fatalf("code = %q, want %q", code, errs.CodeMemoryWriteNotRequested)
				}
				assertEmpty(t, ctx, repo)
			}
		})
	}
}

// An empty quote is not a match, and the distinction matters: a guard that
// treated "" as a substring would accept every unquoted call — which is exactly
// the shape a fabricated request takes.
func TestAnEmptyQuoteIsRefused(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewMemoryRepository()
	svc := newService(t, repo)

	for _, quote := range []string{"", "   ", "\n"} {
		turnCtx := memorywrite.WithTurn(ctx, memorywrite.Turn{
			UserID: fixtureUser, Message: "记住我不吃辣",
		})
		_, err := svc.Save(turnCtx, memorywrite.Request{
			Content:        "不吃辣",
			Type:           domainmemory.MemoryTypeConstraint,
			QuotedUserText: quote,
		})
		if err == nil {
			t.Fatalf("a blank quote (%q) was accepted", quote)
		}
	}
	assertEmpty(t, ctx, repo)
}

// A tool invoked outside a turn finds no message to quote against, and that is
// a refusal rather than a pass: without the turn there is no evidence at all.
func TestSavingOutsideATurnIsRefused(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewMemoryRepository()
	svc := newService(t, repo)

	_, err := svc.Save(ctx, memorywrite.Request{
		Content:        "不吃辣",
		Type:           domainmemory.MemoryTypeConstraint,
		QuotedUserText: "我不吃辣",
	})
	if err == nil {
		t.Fatal("expected a refusal outside a turn")
	}
	if code := errs.CodeOf(err); code != errs.CodeMemoryWriteNotRequested {
		t.Fatalf("code = %q, want %q", code, errs.CodeMemoryWriteNotRequested)
	}
	assertEmpty(t, ctx, repo)
}

// A turn with no user is a turn with nobody to remember anything for.
func TestSavingWithoutAUserIsRefused(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewMemoryRepository()
	svc := newService(t, repo)

	turnCtx := memorywrite.WithTurn(ctx, memorywrite.Turn{Message: "记住我不吃辣"})
	_, err := svc.Save(turnCtx, memorywrite.Request{
		Content:        "不吃辣",
		Type:           domainmemory.MemoryTypeConstraint,
		QuotedUserText: "我不吃辣",
	})
	if err == nil {
		t.Fatal("expected a refusal without a user")
	}
	assertEmpty(t, ctx, repo)
}

// ---- what gets written ----------------------------------------------------

// The type decides how firmly the memory is injected, and the mapping is a
// product decision rather than a per-write judgement the model is entitled to
// make. Every memory written on a user request is stamped as such, which is what
// makes "was this asked for" auditable after the fact.
func TestAUserRequestedMemoryIsStampedAndWeightedByItsType(t *testing.T) {
	cases := map[domainmemory.MemoryType]float64{
		domainmemory.MemoryTypeConstraint: 1.0,
		domainmemory.MemoryTypePreference: 0.9,
		domainmemory.MemoryTypeFact:       0.8,
	}
	for memType, wantConfidence := range cases {
		t.Run(string(memType), func(t *testing.T) {
			repo := testkit.NewMemoryRepository()
			svc := newService(t, repo)

			turnCtx, req := requestWith("记住我不吃辣", "不吃辣", memType)
			result, err := svc.Save(turnCtx, req)
			if err != nil {
				t.Fatalf("Save: %v", err)
			}
			saved := result.Memory
			if saved.ID == "" {
				t.Fatal("the saved memory has no id")
			}
			if saved.Type != memType {
				t.Fatalf("type = %q, want %q", saved.Type, memType)
			}
			if saved.Confidence != wantConfidence {
				t.Fatalf("confidence = %g, want %g", saved.Confidence, wantConfidence)
			}
			if saved.Source != memorywrite.SourceUserRequest {
				t.Fatalf("source = %q, want %q", saved.Source, memorywrite.SourceUserRequest)
			}
			if saved.Content != "不吃辣" {
				t.Fatalf("content = %q", saved.Content)
			}
			if saved.CreatedAt.IsZero() || saved.UpdatedAt.IsZero() {
				t.Fatalf("timestamps not stamped: %+v", saved)
			}
			if result.Refreshed {
				t.Fatal("a first save reported itself as a refresh")
			}
		})
	}
}

// Saying the same thing twice refreshes the row instead of adding a second one.
// Two identical memories would inject the same sentence twice, which reads to
// the model as emphasis the user never asked for.
func TestSavingTheSameMemoryTwiceRefreshesItInPlace(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewMemoryRepository()
	svc := newService(t, repo)

	firstCtx, firstReq := requestWith("记住我不吃辣", "不吃辣", domainmemory.MemoryTypeConstraint)
	first, err := svc.Save(firstCtx, firstReq)
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}

	memories, err := repo.List(ctx, fixtureUser)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("memories = %d, want 1", len(memories))
	}
	firstStamp := memories[0].UpdatedAt

	// The store advances timestamps monotonically, so a second write at the
	// same wall-clock instant still moves forward.
	second, err := svc.Save(firstCtx, firstReq)
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if !second.Refreshed {
		t.Fatal("the repeat did not report itself as a refresh")
	}
	if second.Memory.ID != first.Memory.ID {
		t.Fatalf("the repeat created a second row: %q then %q", first.Memory.ID, second.Memory.ID)
	}

	memories, err = repo.List(ctx, fixtureUser)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("memories = %d after the repeat, want 1", len(memories))
	}
	if !memories[0].UpdatedAt.After(firstStamp) {
		t.Fatalf("updated_at did not advance: %s then %s", firstStamp, memories[0].UpdatedAt)
	}
	if !memories[0].CreatedAt.Equal(first.Memory.CreatedAt) {
		t.Fatal("the refresh rewrote created_at")
	}
}

// The refresh key is type plus content, exactly. A preference and a constraint
// that read the same are two different instructions, and two sentences that
// differ are two memories — folding them would silently rewrite what was asked.
func TestTheRefreshKeyIsTheTypeAndTheExactContent(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewMemoryRepository()
	svc := newService(t, repo)

	turnCtx := memorywrite.WithTurn(ctx, memorywrite.Turn{
		UserID: fixtureUser, Message: "记住我不吃辣",
	})
	for _, req := range []memorywrite.Request{
		{Content: "不吃辣", Type: domainmemory.MemoryTypeConstraint, QuotedUserText: "记住我不吃辣"},
		{Content: "不吃辣", Type: domainmemory.MemoryTypePreference, QuotedUserText: "记住我不吃辣"},
		{Content: "不吃辣。", Type: domainmemory.MemoryTypeConstraint, QuotedUserText: "记住我不吃辣"},
	} {
		if _, err := svc.Save(turnCtx, req); err != nil {
			t.Fatalf("Save(%s/%s): %v", req.Type, req.Content, err)
		}
	}
	memories, err := repo.List(ctx, fixtureUser)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(memories) != 3 {
		t.Fatalf("memories = %d, want 3 distinct rows", len(memories))
	}
}

func TestSaveRejectsMalformedRequests(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewMemoryRepository()
	svc := newService(t, repo)

	turnCtx := memorywrite.WithTurn(ctx, memorywrite.Turn{
		UserID: fixtureUser, Message: "记住我不吃辣",
	})
	long := strings.Repeat("辣", memorywrite.MaxContentRunes+1)
	cases := map[string]memorywrite.Request{
		"no content":       {Type: domainmemory.MemoryTypeConstraint, QuotedUserText: "记住我不吃辣"},
		"blank content":    {Content: "   ", Type: domainmemory.MemoryTypeConstraint, QuotedUserText: "记住我不吃辣"},
		"content too long": {Content: long, Type: domainmemory.MemoryTypeConstraint, QuotedUserText: "记住我不吃辣"},
		"unknown type":     {Content: "不吃辣", Type: "opinion", QuotedUserText: "记住我不吃辣"},
		"no type":          {Content: "不吃辣", QuotedUserText: "记住我不吃辣"},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.Save(turnCtx, req); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}
	assertEmpty(t, ctx, repo)
}

// The limit counts characters, not bytes. A Chinese sentence is three times its
// length in bytes, so a limit that counted bytes would cut it off at a third of
// the room.
func TestTheContentLimitCountsCharacters(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewMemoryRepository()
	svc := newService(t, repo)

	turnCtx := memorywrite.WithTurn(ctx, memorywrite.Turn{
		UserID: fixtureUser, Message: "记住我不吃辣",
	})
	exactly := strings.Repeat("辣", memorywrite.MaxContentRunes)
	result, err := svc.Save(turnCtx, memorywrite.Request{
		Content: exactly, Type: domainmemory.MemoryTypeConstraint, QuotedUserText: "记住我不吃辣",
	})
	if err != nil {
		t.Fatalf("a memory of exactly %d characters was refused: %v", memorywrite.MaxContentRunes, err)
	}
	if got := len([]rune(result.Memory.Content)); got != memorywrite.MaxContentRunes {
		t.Fatalf("stored %d characters, want %d", got, memorywrite.MaxContentRunes)
	}
}

// ---- the user-visible edit path -------------------------------------------

// An edit keeps the row. Soft-deleting the old memory and inserting a new one
// would show the user two rows for one preference, which is not what "I changed
// my mind about the wording" means.
func TestAnEditKeepsTheRowAndFollowsTheType(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewMemoryRepository()
	svc := newService(t, repo)

	turnCtx, req := requestWith("记住我不吃辣", "不吃辣", domainmemory.MemoryTypePreference)
	saved, err := svc.Save(turnCtx, req)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	reworded := "不吃辣，也不吃香菜"
	constraint := domainmemory.MemoryTypeConstraint
	updated, err := svc.Update(ctx, fixtureUser, saved.Memory.ID, memorywrite.Set{
		Content: &reworded,
		Type:    &constraint,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.ID != saved.Memory.ID {
		t.Fatalf("the edit changed the id: %q then %q", saved.Memory.ID, updated.ID)
	}
	if updated.Content != reworded {
		t.Fatalf("content = %q, want %q", updated.Content, reworded)
	}
	if updated.Type != domainmemory.MemoryTypeConstraint {
		t.Fatalf("type = %q, want constraint", updated.Type)
	}
	// The confidence is the type's meaning, not a separately editable number:
	// letting the two drift would let a constraint carry a preference's weight.
	if updated.Confidence != memorywrite.ConfidenceByType[domainmemory.MemoryTypeConstraint] {
		t.Fatalf("confidence = %g, want the constraint's weight", updated.Confidence)
	}
	if !updated.CreatedAt.Equal(saved.Memory.CreatedAt) {
		t.Fatal("the edit rewrote created_at")
	}
	if !updated.UpdatedAt.After(saved.Memory.UpdatedAt) {
		t.Fatalf("updated_at did not advance: %s then %s", saved.Memory.UpdatedAt, updated.UpdatedAt)
	}
}

// A partial edit changes only what it names. The pointer fields exist for this:
// a client that sent only memory_type must not blank the content as a side
// effect.
func TestAPartialEditLeavesTheOtherFieldAlone(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewMemoryRepository()
	svc := newService(t, repo)

	turnCtx, req := requestWith("记住我不吃辣", "不吃辣", domainmemory.MemoryTypePreference)
	saved, err := svc.Save(turnCtx, req)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	constraint := domainmemory.MemoryTypeConstraint
	onlyType, err := svc.Update(ctx, fixtureUser, saved.Memory.ID, memorywrite.Set{Type: &constraint})
	if err != nil {
		t.Fatalf("Update(type only): %v", err)
	}
	if onlyType.Content != saved.Memory.Content {
		t.Fatalf("a type-only edit changed the content: %q", onlyType.Content)
	}

	reworded := "完全不吃辣"
	onlyContent, err := svc.Update(ctx, fixtureUser, saved.Memory.ID, memorywrite.Set{Content: &reworded})
	if err != nil {
		t.Fatalf("Update(content only): %v", err)
	}
	if onlyContent.Type != domainmemory.MemoryTypeConstraint {
		t.Fatalf("a content-only edit changed the type: %q", onlyContent.Type)
	}
	if onlyContent.Content != reworded {
		t.Fatalf("content = %q, want %q", onlyContent.Content, reworded)
	}
}

// Another user's memory id is not found rather than forbidden. A distinct 403
// would confirm the id exists, which is the one thing the scoping is for.
func TestEditingAnotherUsersMemoryIsNotFound(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewMemoryRepository()
	svc := newService(t, repo)

	turnCtx := memorywrite.WithTurn(ctx, memorywrite.Turn{
		UserID: "user-1", Message: "记住我不吃辣",
	})
	saved, err := svc.Save(turnCtx, memorywrite.Request{
		Content: "不吃辣", Type: domainmemory.MemoryTypeConstraint, QuotedUserText: "记住我不吃辣",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	reworded := "不吃香菜"
	_, err = svc.Update(ctx, "user-2", saved.Memory.ID, memorywrite.Set{Content: &reworded})
	if err == nil {
		t.Fatal("another user's memory id was editable")
	}
	if code := errs.CodeOf(err); code != errs.CodeNotFound {
		t.Fatalf("code = %q, want %q", code, errs.CodeNotFound)
	}

	// The owner's copy is untouched.
	memories, listErr := repo.List(ctx, "user-1")
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(memories) != 1 || memories[0].Content != "不吃辣" {
		t.Fatalf("the refused edit changed the owner's memory: %+v", memories)
	}
}

func TestUpdateRejectsMalformedEdits(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewMemoryRepository()
	svc := newService(t, repo)

	turnCtx := memorywrite.WithTurn(ctx, memorywrite.Turn{
		UserID: fixtureUser, Message: "记住我不吃辣",
	})
	saved, err := svc.Save(turnCtx, memorywrite.Request{
		Content: "不吃辣", Type: domainmemory.MemoryTypeConstraint, QuotedUserText: "记住我不吃辣",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	blank := "   "
	long := strings.Repeat("辣", memorywrite.MaxContentRunes+1)
	unknown := domainmemory.MemoryType("opinion")
	cases := []struct {
		name string
		user string
		id   string
		set  memorywrite.Set
	}{
		{name: "nothing to change", user: fixtureUser, id: saved.Memory.ID},
		{name: "blank content", user: fixtureUser, id: saved.Memory.ID, set: memorywrite.Set{Content: &blank}},
		{name: "content too long", user: fixtureUser, id: saved.Memory.ID, set: memorywrite.Set{Content: &long}},
		{name: "unknown type", user: fixtureUser, id: saved.Memory.ID, set: memorywrite.Set{Type: &unknown}},
		{name: "no user", id: saved.Memory.ID, set: memorywrite.Set{Content: &blank}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Update(ctx, tc.user, tc.id, tc.set); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}
}

// ---- helpers --------------------------------------------------------------

func assertEmpty(t *testing.T, ctx context.Context, repo store.MemoryRepository) {
	t.Helper()
	memories, err := repo.List(ctx, fixtureUser)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(memories) != 0 {
		t.Fatalf("a refused write left %d memories: %+v", len(memories), memories)
	}
}
