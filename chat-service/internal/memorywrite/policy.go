// Package memorywrite owns the one path by which a long-term memory is created:
// the user asking for it in their own words.
//
// The package exists because "only save what the user explicitly asked to
// remember" is a requirement that a prompt cannot enforce. A model that decides
// a preference is worth keeping will say so in its tool call, and nothing in the
// call distinguishes it from a request the user actually made. So the
// requirement is turned into an input check: the model must quote the part of
// the user's message that asked for the memory, and the quote must be a
// substring of that message. A fabricated condition has nothing to quote.
//
// Nothing else in the system writes memories. There is deliberately no
// summariser, no "this search looked like a preference, let's keep it", and no
// decay: PRD §3.5 makes "do not promote a single search's conditions into a
// long-term preference" a red line, and the cheapest way to hold a red line is
// to have no code on the other side of it.
package memorywrite

import (
	"context"
	"strings"
	"unicode"

	"github.com/zed1995/platepilot/shared/domain/errs"
	domainmemory "github.com/zed1995/platepilot/shared/domain/memory"
	"github.com/zed1995/platepilot/shared/idgen"
	"github.com/zed1995/platepilot/shared/store"
)

// Request is one user-requested memory.
type Request struct {
	// Content is the memory itself, as it will be injected.
	Content string
	// Type classifies it, which decides how firmly the injected segment tells
	// the model to treat it.
	Type domainmemory.MemoryType
	// QuotedUserText is the part of the user's own message that asked for this.
	// It is checked against the turn's message and then discarded — it is
	// evidence, not content, and storing it would put the model's quote in the
	// user's memory list.
	QuotedUserText string
}

// Limits on what can be remembered.
const (
	// MaxContentRunes bounds one memory. The bound is on runes rather than
	// bytes because a Chinese sentence is three times its length in bytes, and
	// a limit that counted bytes would cut it off at a third of the room.
	MaxContentRunes = 200
	// maxQuoteRunes bounds the evidence. A quote longer than this is not a
	// quote of the part that asked, it is the whole message pasted back.
	maxQuoteRunes = 400
)

// SourceUserRequest is the Source stamped on every memory this package writes.
//
// It is a constant rather than a literal at the call site because it is what
// makes the red line auditable after the fact: a row whose Source is anything
// else was not written by a user request. Nothing else writes Source.
const SourceUserRequest = "user_request"

// ConfidenceByType is the confidence a memory of each type carries.
//
// The numbers encode how firmly the injected segment should treat the memory,
// not how likely it is to be true: a constraint is a hard requirement the model
// must not violate, a preference is a default, and a fact is background. They
// are constants because they are a product decision, not a per-write judgement
// the model is entitled to make.
var ConfidenceByType = map[domainmemory.MemoryType]float64{
	domainmemory.MemoryTypeConstraint: 1.0,
	domainmemory.MemoryTypePreference: 0.9,
	domainmemory.MemoryTypeFact:       0.8,
}

// Turn is the turn-scoped context a memory write is checked against: who is
// asking, and in what words.
//
// The two travel together because they are one fact — "this user asked for this
// in their own message" — and a guard that could receive one without the other
// would have to decide what a missing half means. They ride on the context for
// the same reason the turn's plan does: a value that arrived as a tool argument
// would be the model's own copy, which is exactly what is being checked.
type Turn struct {
	UserID string
	// Message is the user's message for this turn, verbatim.
	Message string
}

// turnKey carries the turn to the tool that needs it.
type turnKey struct{}

// WithTurn attaches the current turn to a context.
func WithTurn(ctx context.Context, turn Turn) context.Context {
	return context.WithValue(ctx, turnKey{}, turn)
}

// TurnFromContext reports the turn a tool call is running inside.
//
// The second return value distinguishes "the user said nothing" from "there is
// no turn": a tool invoked outside a turn finds none, and a guard that treated
// the zero Turn as a match would accept every quote.
func TurnFromContext(ctx context.Context) (Turn, bool) {
	turn, ok := ctx.Value(turnKey{}).(Turn)
	return turn, ok
}

// Service writes user-requested memories.
type Service struct {
	memories store.MemoryRepository
	// newID mints the identity of a new memory. It is the service's job rather
	// than the repository's because the caller has to answer with the row it
	// wrote: the repository's Upsert takes the memory by value and assigns an id
	// to its own copy, so a caller that let it choose would be holding a memory
	// it cannot name.
	newID func() string
}

// NewService builds the service. A nil repository is a programming error rather
// than a degraded deployment: the tool is registered only when the port exists.
func NewService(memories store.MemoryRepository) (*Service, error) {
	if memories == nil {
		return nil, errs.New(errs.CodeInvalidArgument,
			"memorywrite requires a memory repository")
	}
	return &Service{memories: memories, newID: idgen.NewUUID}, nil
}

// Result is one saved memory plus what the write did.
type Result struct {
	Memory domainmemory.Memory
	// Refreshed reports that an identical memory already existed and had its
	// timestamp moved forward instead of a second row being created.
	Refreshed bool
}

// Save validates the request against the user's own message and stores it.
//
// The order matters: the guard runs before anything is read or written, so a
// fabricated request costs nothing and leaves no trace.
func (s *Service) Save(ctx context.Context, req Request) (Result, error) {
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return Result{}, errs.New(errs.CodeValidationFailed, "content is required")
	}
	if countRunes(content) > MaxContentRunes {
		return Result{}, errs.Newf(errs.CodeValidationFailed,
			"content must be at most %d characters (got %d)",
			MaxContentRunes, countRunes(content))
	}
	confidence, ok := ConfidenceByType[req.Type]
	if !ok {
		return Result{}, errs.Newf(errs.CodeValidationFailed,
			"memory_type must be %q, %q or %q (got %q)",
			domainmemory.MemoryTypePreference, domainmemory.MemoryTypeConstraint,
			domainmemory.MemoryTypeFact, req.Type)
	}
	turn, err := checkQuote(ctx, req.QuotedUserText)
	if err != nil {
		return Result{}, err
	}
	// A memory belongs to a user; an anonymous turn has nobody to belong to.
	// This is refused here rather than at the tool boundary so the rule holds
	// for every caller of Save, not only the one tool that exists today.
	if strings.TrimSpace(turn.UserID) == "" {
		return Result{}, errs.New(errs.CodeInvalidArgument,
			"saving a memory needs a user to save it for")
	}

	// The refresh check is a read-then-write. Its worst case is a duplicate row
	// when the same request is saved twice concurrently, and that is benign
	// enough to accept: two identical memories inject the same sentence twice,
	// whereas a lock held across a model-driven write would be a worse trade
	// for a case the user has to cause deliberately.
	existing, err := s.findExisting(ctx, turn.UserID, req.Type, content)
	if err != nil {
		return Result{}, err
	}
	memory := domainmemory.Memory{
		ID:         existingID(existing, s.newID),
		UserID:     turn.UserID,
		Type:       req.Type,
		Content:    content,
		Source:     SourceUserRequest,
		Confidence: confidence,
	}
	if err := s.memories.Upsert(ctx, memory); err != nil {
		return Result{}, err
	}
	// Upsert stamps the timestamps on its own copy and never echoes them back
	// through the repository interface. Re-reading is what lets the caller
	// answer with the row as stored rather than with the row as requested.
	saved, err := s.reload(ctx, turn.UserID, memory.ID)
	if err != nil {
		return Result{}, err
	}
	return Result{Memory: saved, Refreshed: existing != nil}, nil
}

// Set is what the PATCH endpoint asks for: an edit of an existing memory.
type Set struct {
	// Content and Type are pointers so "omitted" is distinguishable from
	// "cleared". A PATCH that sent neither would otherwise blank the memory.
	Content *string
	Type    *domainmemory.MemoryType
}

// Update applies a user's edit to one of their memories.
//
// The lookup is scoped to the user, and that is the whole authorization model:
// a memory id belonging to someone else is not found rather than forbidden, so
// the endpoint cannot be used to probe which ids exist.
func (s *Service) Update(
	ctx context.Context, userID, memoryID string, set Set,
) (domainmemory.Memory, error) {
	if strings.TrimSpace(userID) == "" {
		return domainmemory.Memory{}, errs.New(errs.CodeInvalidArgument,
			"updating a memory needs a user whose memory it is")
	}
	if strings.TrimSpace(memoryID) == "" {
		return domainmemory.Memory{}, errs.New(errs.CodeInvalidArgument, "memory_id is required")
	}
	if set.Content == nil && set.Type == nil {
		return domainmemory.Memory{}, errs.New(errs.CodeValidationFailed,
			"at least one of content or memory_type is required")
	}
	current, err := s.find(ctx, userID, memoryID)
	if err != nil {
		return domainmemory.Memory{}, err
	}

	updated := current
	if set.Content != nil {
		content := strings.TrimSpace(*set.Content)
		if content == "" {
			return domainmemory.Memory{}, errs.New(errs.CodeValidationFailed,
				"content must not be empty")
		}
		if countRunes(content) > MaxContentRunes {
			return domainmemory.Memory{}, errs.Newf(errs.CodeValidationFailed,
				"content must be at most %d characters (got %d)",
				MaxContentRunes, countRunes(content))
		}
		updated.Content = content
	}
	if set.Type != nil {
		confidence, ok := ConfidenceByType[*set.Type]
		if !ok {
			return domainmemory.Memory{}, errs.Newf(errs.CodeValidationFailed,
				"memory_type must be %q, %q or %q (got %q)",
				domainmemory.MemoryTypePreference, domainmemory.MemoryTypeConstraint,
				domainmemory.MemoryTypeFact, *set.Type)
		}
		updated.Type = *set.Type
		// The confidence follows the type rather than being edited separately:
		// it is the type's meaning, and letting the two drift would let a
		// constraint carry a preference's weight.
		updated.Confidence = confidence
	}
	// An edit keeps the row. Soft-deleting the old memory and inserting a new
	// one would show the user two rows for one preference — one of them gone —
	// which is not what "I changed my mind about the wording" means.
	if err := s.memories.Upsert(ctx, updated); err != nil {
		return domainmemory.Memory{}, err
	}
	return s.reload(ctx, userID, memoryID)
}

// find returns one of the user's memories, or not_found.
//
// It reads the list rather than a get-by-id because the repository has no such
// method and the list is already the smallest unit that carries the ownership
// scoping. The list is bounded in practice by the size of one user's memory —
// a handful of sentences — so the scan is not the place to spend an index.
func (s *Service) find(
	ctx context.Context, userID, memoryID string,
) (domainmemory.Memory, error) {
	memories, err := s.memories.List(ctx, userID)
	if err != nil {
		return domainmemory.Memory{}, err
	}
	for _, mem := range memories {
		if mem.ID == memoryID {
			return mem, nil
		}
	}
	return domainmemory.Memory{}, errs.Newf(errs.CodeNotFound,
		"memory %q not found", memoryID)
}

// findExisting returns the user's memory with the same type and content, or nil.
func (s *Service) findExisting(
	ctx context.Context, userID string, memType domainmemory.MemoryType, content string,
) (*domainmemory.Memory, error) {
	memories, err := s.memories.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range memories {
		// Exact match, including case: "不吃辣" and "不吃辣。" are two memories
		// because they are two sentences, and folding them would silently
		// rewrite what the user asked to remember.
		if memories[i].Type == memType && memories[i].Content == content {
			return &memories[i], nil
		}
	}
	return nil, nil
}

// reload reads a memory back so the caller answers with what was stored.
func (s *Service) reload(
	ctx context.Context, userID, memoryID string,
) (domainmemory.Memory, error) {
	return s.find(ctx, userID, memoryID)
}

// existingID reuses a refreshed memory's id, and mints one for a new memory.
//
// Reusing is what makes a repeat a refresh rather than a second row. Minting
// here rather than leaving it to the store is what lets the caller answer with
// the identity of what it just wrote.
func existingID(existing *domainmemory.Memory, newID func() string) string {
	if existing != nil {
		return existing.ID
	}
	return newID()
}

// checkQuote is the guard: the model's evidence has to be the user's own words.
//
// The comparison normalizes whitespace and case but nothing else. Normalizing
// more — stripping punctuation, say — would let "我不吃辣" match a message that
// said "我不吃辣的"，and the sentence a user is agreeing to be remembered is
// exactly the thing that must not be paraphrased.
func checkQuote(ctx context.Context, quoted string) (Turn, error) {
	quote := strings.TrimSpace(quoted)
	if quote == "" {
		return Turn{}, errs.New(errs.CodeMemoryWriteNotRequested,
			"quoted_user_text is required: a memory may only be saved when the user asked for it")
	}
	if countRunes(quote) > maxQuoteRunes {
		return Turn{}, errs.Newf(errs.CodeValidationFailed,
			"quoted_user_text must be at most %d characters (got %d)",
			maxQuoteRunes, countRunes(quote))
	}
	turn, ok := TurnFromContext(ctx)
	if !ok {
		return Turn{}, errs.New(errs.CodeMemoryWriteNotRequested,
			"该工具只能在用户对话轮次中执行")
	}
	if !containsNormalized(turn.Message, quote) {
		return Turn{}, errs.Newf(errs.CodeMemoryWriteNotRequested,
			"quoted_user_text %q 不在本轮用户消息中；只能记录用户明确要求记住的内容", quote)
	}
	return turn, nil
}

// containsNormalized reports whether haystack contains needle, comparing on
// whitespace-collapsed, lower-cased text.
func containsNormalized(haystack, needle string) bool {
	return strings.Contains(normalize(haystack), normalize(needle))
}

// normalize collapses every run of whitespace to a single space and lower-cases.
//
// Case and whitespace are presentation, not meaning: a user typing "不吃辣" and a
// model quoting "不吃辣 " with a trailing space are describing the same request,
// and a guard that rejected that would look like a bug. Everything else is kept.
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// countRunes counts characters, which is what a length limit means to a person.
func countRunes(s string) int { return len([]rune(s)) }
