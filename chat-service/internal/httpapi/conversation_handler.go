package httpapi

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"

	"github.com/zed1995/platepilot/chat-service/internal/httperr"
	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domainmemory "github.com/zed1995/platepilot/shared/domain/memory"
	"github.com/zed1995/platepilot/shared/requestctx"
)

// HeaderUserID carries the caller's user identity until M6 introduces real
// authentication. It is intentionally a plain header: there is no principal
// to verify yet, and pretending otherwise would give a false sense of access
// control.
const HeaderUserID = "X-User-ID"

// Message limits.
const (
	// maxMessageRunes bounds one user message. It rejects paste accidents and
	// obvious abuse without clipping legitimate questions.
	maxMessageRunes = 4000
	// defaultHistoryPageSize / maxHistoryPageSize bound transcript replay.
	defaultHistoryPageSize = 20
	maxHistoryPageSize     = 100
)

// ChatService is the conversational application surface this transport talks
// to: thread metadata, transcript pages, the streaming turn, and user
// memories. It is an interface so the HTTP layer is fully testable against a
// scripted double without a runner, a model, or a database.
type ChatService interface {
	CreateThread(ctx context.Context, userID, title string) (conversation.Conversation, error)
	GetThread(ctx context.Context, threadID string) (ThreadDetail, error)
	ListMessages(ctx context.Context, threadID string, limit int, beforeID string) (MessagePage, error)
	// SendMessage runs one turn. emit is invoked serially, in event order, on
	// the calling goroutine; a non-nil emit error means the client is gone and
	// the service must abandon the turn.
	SendMessage(ctx context.Context, in SendMessageInput, emit func(StreamEvent) error) error
	// ConfirmAction applies the user's answer to the thread's pending action.
	// It is separate from SendMessage because it is not a turn: no model runs,
	// and the outcome is a decision rather than a reply.
	ConfirmAction(ctx context.Context, in ConfirmInput) (ConfirmResult, error)
	ListMemories(ctx context.Context, userID string) (MemoryPage, error)
	// UpdateMemory applies an edit to one of the user's memories. The user id
	// scopes the lookup, so an id belonging to someone else is not found.
	UpdateMemory(ctx context.Context, in UpdateMemoryInput) (MemoryView, error)
	DeleteMemory(ctx context.Context, userID, memoryID string) error
}

// SendMessageInput identifies the thread and the new user message. TraceID
// comes from the request-id middleware so the run audit row correlates with
// HTTP access logs.
type SendMessageInput struct {
	ThreadID string
	UserID   string
	TraceID  string
	Content  string
}

// ThreadDetail pairs thread metadata with a summary of its latest checkpoint.
// The checkpoint pointer is nil for a thread whose first turn has never
// finished.
type ThreadDetail struct {
	Conversation conversation.Conversation
	Checkpoint   *conversation.Checkpoint
}

// MessagePage is one chronological page of a thread's transcript.
type MessagePage struct {
	Messages []conversation.Message
}

// MemoryPage lists one user's live memories.
type MemoryPage struct {
	Memories []MemoryView
}

// MemoryView is the user-facing memory shape. The embedding vector never
// leaves the service boundary: it is retrieval infrastructure, not user data.
type MemoryView struct {
	ID         string    `json:"id"`
	Type       string    `json:"memory_type"`
	Content    string    `json:"content"`
	Source     string    `json:"source,omitempty"`
	Confidence float64   `json:"confidence"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// UpdateMemoryInput is one requested edit.
//
// The two fields are pointers so "not mentioned" is distinguishable from
// "cleared". A PATCH that sent neither is refused rather than answered with a
// no-op, because a client that meant to change something and got a 200 back
// would have no way to learn that nothing changed.
type UpdateMemoryInput struct {
	UserID   string
	MemoryID string
	Content  *string
	Type     *domainmemory.MemoryType
}

// ---------------------------------------------------------------------------
// Wire DTOs
// ---------------------------------------------------------------------------

type createThreadRequest struct {
	Title string `json:"title,omitempty"`
}

func (r createThreadRequest) Validate() error {
	if utf8.RuneCountInString(r.Title) > 200 {
		return errs.New(errs.CodeInvalidArgument, "title must be at most 200 characters")
	}
	return nil
}

type sendMessageRequest struct {
	Content string `json:"content"`
}

func (r sendMessageRequest) Validate() error {
	content := trimSpace(r.Content)
	if content == "" {
		return errs.New(errs.CodeInvalidArgument, "message content must not be empty")
	}
	if utf8.RuneCountInString(content) > maxMessageRunes {
		return errs.Newf(errs.CodeInvalidArgument,
			"message content must be at most %d characters", maxMessageRunes)
	}
	return nil
}

// checkpointView is the checkpoint summary embedded in a thread response.
type checkpointView struct {
	Version              int64     `json:"version"`
	State                string    `json:"state"`
	PendingAction        string    `json:"pending_action,omitempty"`
	MissingSlots         []string  `json:"missing_slots,omitempty"`
	EvidenceIDs          []int64   `json:"evidence_ids,omitempty"`
	SelectedRestaurantID int64     `json:"selected_restaurant_id,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}

type threadResponse struct {
	ThreadID      string          `json:"thread_id"`
	UserID        string          `json:"user_id,omitempty"`
	Title         string          `json:"title,omitempty"`
	CurrentState  string          `json:"current_state"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	LastMessageAt time.Time       `json:"last_message_at,omitempty"`
	Checkpoint    *checkpointView `json:"checkpoint,omitempty"`
}

type messageView struct {
	MessageID   string    `json:"message_id"`
	Role        string    `json:"role"`
	Content     string    `json:"content"`
	EvidenceIDs []int64   `json:"evidence_ids,omitempty"`
	Seq         int64     `json:"seq"`
	CreatedAt   time.Time `json:"created_at"`
}

type messageListResponse struct {
	Messages []messageView `json:"messages"`
}

type memoryListResponse struct {
	Memories []MemoryView `json:"memories"`
}

func toThreadResponse(detail ThreadDetail) threadResponse {
	resp := threadResponse{
		ThreadID:      detail.Conversation.ThreadID,
		UserID:        detail.Conversation.UserID,
		Title:         detail.Conversation.Title,
		CurrentState:  string(detail.Conversation.CurrentState),
		CreatedAt:     detail.Conversation.CreatedAt,
		UpdatedAt:     detail.Conversation.UpdatedAt,
		LastMessageAt: detail.Conversation.LastMessageAt,
	}
	if detail.Checkpoint != nil {
		cp := detail.Checkpoint
		resp.Checkpoint = &checkpointView{
			Version:              cp.Version,
			State:                string(cp.State),
			PendingAction:        cp.PendingAction,
			MissingSlots:         cp.MissingSlots,
			EvidenceIDs:          cp.EvidenceIDs,
			SelectedRestaurantID: cp.SelectedRestaurantID,
			CreatedAt:            cp.CreatedAt,
		}
	}
	return resp
}

func toMessageListResponse(page MessagePage) messageListResponse {
	views := make([]messageView, 0, len(page.Messages))
	for _, msg := range page.Messages {
		views = append(views, messageView{
			MessageID:   msg.MessageID,
			Role:        msg.Role,
			Content:     msg.Content,
			EvidenceIDs: msg.EvidenceIDs,
			Seq:         msg.Seq,
			CreatedAt:   msg.CreatedAt,
		})
	}
	return messageListResponse{Messages: views}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// registerChatRoutes mounts the whole conversational surface under /v1.
func registerChatRoutes(h *server.Hertz, svc ChatService) {
	v1 := h.Group("/v1")
	v1.POST("/conversations", CreateThreadHandler(svc))
	v1.GET("/conversations/:id", GetThreadHandler(svc))
	v1.GET("/conversations/:id/messages", ListMessagesHandler(svc))
	v1.POST("/conversations/:id/messages", SendMessageHandler(svc))
	// The decision endpoint sits beside the message endpoint rather than inside
	// it: a confirmation is answered by its own route, and a thread that has
	// nothing pending is told so instead of having its text re-read as a yes.
	v1.POST("/conversations/:id/confirm", ConfirmHandler(svc))
	v1.GET("/memories", ListMemoriesHandler(svc))
	v1.PATCH("/memories/:memory_id", UpdateMemoryHandler(svc))
	v1.DELETE("/memories/:memory_id", DeleteMemoryHandler(svc))
}

// CreateThreadHandler answers POST /v1/conversations.
func CreateThreadHandler(svc ChatService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		var body createThreadRequest
		if err := BindAndValidate(c, &body); err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		conv, err := svc.CreateThread(ctx, userIDFromContext(ctx), trimSpace(body.Title))
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		c.JSON(201, toThreadResponse(ThreadDetail{Conversation: conv}))
	}
}

// GetThreadHandler answers GET /v1/conversations/:id.
func GetThreadHandler(svc ChatService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		detail, err := svc.GetThread(ctx, c.Param("id"))
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		c.JSON(200, toThreadResponse(detail))
	}
}

// ListMessagesHandler answers GET /v1/conversations/:id/messages.
//
// Pagination is a simple before-id cursor: responses return the newest limit
// messages strictly older than before_id, oldest first. Without before_id the
// newest page is returned. An unknown thread surfaces not_found rather than an
// empty page, so a typo never looks like an empty conversation.
func ListMessagesHandler(svc ChatService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		limit, err := queryIntValue(c, "limit")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		if limit == 0 {
			limit = defaultHistoryPageSize
		}
		if limit > maxHistoryPageSize {
			limit = maxHistoryPageSize
		}
		page, err := svc.ListMessages(ctx, c.Param("id"), limit, queryValue(c, "before_id"))
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		resp := toMessageListResponse(page)
		if resp.Messages == nil {
			resp.Messages = []messageView{}
		}
		c.JSON(200, resp)
	}
}

// SendMessageHandler answers POST /v1/conversations/:id/messages with a
// text/event-stream of turn events.
//
// Validation and thread-existence failures happen before the stream opens and
// therefore use the normal JSON error envelope. Everything after the first
// byte — including a model failure mid-turn — is delivered as an error event
// inside the stream, because the client is already parsing SSE frames.
func SendMessageHandler(svc ChatService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		var body sendMessageRequest
		if err := BindAndValidate(c, &body); err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		rc, _ := requestctx.FromContext(ctx)
		in := SendMessageInput{
			ThreadID: c.Param("id"),
			UserID:   userIDFromContext(ctx),
			TraceID:  rc.TraceID,
			Content:  trimSpace(body.Content),
		}
		// Pre-stream existence check: posting into a mistyped thread must 404
		// as JSON, not stream an error event into a conversation that does not
		// exist.
		if _, err := svc.GetThread(ctx, in.ThreadID); err != nil {
			httperr.Write(ctx, c, err)
			return
		}

		sw := newStreamWriter(c)
		stopHeartbeat := make(chan struct{})
		go runHeartbeats(ctx, sw, stopHeartbeat)

		emitErr := svc.SendMessage(ctx, in, sw.write)
		close(stopHeartbeat)
		// A canceled context is a disconnected client: no one is left to read
		// an error frame, and writing one may itself fail on the dead conn.
		if emitErr != nil && ctx.Err() == nil {
			_ = sw.write(StreamEvent{
				Type:    StreamError,
				Code:    string(errs.CodeOf(emitErr)),
				Message: httperr.ClientMessage(emitErr),
			})
		}
	}
}

// userIDFromContext reads the placeholder identity the request-id middleware
// copied from X-User-ID. Empty means an anonymous turn: memories are skipped
// but the conversation itself is still allowed.
func userIDFromContext(ctx context.Context) string {
	rc, _ := requestctx.FromContext(ctx)
	return rc.UserID
}

func trimSpace(s string) string {
	return strings.TrimSpace(s)
}
