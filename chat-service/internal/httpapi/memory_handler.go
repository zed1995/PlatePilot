package httpapi

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/zed1995/platepilot/chat-service/internal/httperr"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domainmemory "github.com/zed1995/platepilot/shared/domain/memory"
)

// ListMemoriesHandler answers GET /v1/memories.
//
// The caller identity is required: a memory list without a user would have to
// guess whose memories to return. Until M6 ships real authentication the
// identity is the X-User-ID placeholder header.
func ListMemoriesHandler(svc ChatService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		userID := userIDFromContext(ctx)
		if userID == "" {
			WriteAndAbort(ctx, c, errs.New(errs.CodeInvalidArgument,
				HeaderUserID+" header is required to list memories"))
			return
		}
		page, err := svc.ListMemories(ctx, userID)
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		if page.Memories == nil {
			// An empty list is an empty array: a new user owns zero memories,
			// not a null response the client has to special-case.
			page.Memories = []MemoryView{}
		}
		c.JSON(http.StatusOK, memoryListResponse{Memories: page.Memories})
	}
}

// updateMemoryRequest is the PATCH body.
//
// Both fields are pointers, so an omitted field is absent from the JSON and
// distinguishable from one sent as an empty string. Without that, a client that
// sent only memory_type would blank the content as a side effect.
type updateMemoryRequest struct {
	Content    *string `json:"content"`
	MemoryType *string `json:"memory_type"`
}

func (r updateMemoryRequest) Validate() error {
	if r.Content == nil && r.MemoryType == nil {
		return errs.New(errs.CodeValidationFailed,
			"at least one of content or memory_type is required")
	}
	if r.Content != nil && trimSpace(*r.Content) == "" {
		return errs.New(errs.CodeValidationFailed, "content must not be empty")
	}
	return nil
}

// UpdateMemoryHandler answers PATCH /v1/memories/:memory_id.
//
// The caller identity is required for the same reason the list requires it, and
// it is also the whole authorization model: the id is scoped by the user inside
// the service, so editing another user's memory surfaces not_found rather than
// forbidden. A distinct 403 would confirm the id exists.
func UpdateMemoryHandler(svc ChatService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		userID := userIDFromContext(ctx)
		if userID == "" {
			WriteAndAbort(ctx, c, errs.New(errs.CodeInvalidArgument,
				HeaderUserID+" header is required to update a memory"))
			return
		}
		memoryID := c.Param("memory_id")
		if memoryID == "" {
			WriteAndAbort(ctx, c, errs.New(errs.CodeInvalidArgument, "memory_id is required"))
			return
		}
		var body updateMemoryRequest
		if err := BindAndValidate(c, &body); err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		in := UpdateMemoryInput{UserID: userID, MemoryID: memoryID, Content: body.Content}
		if body.MemoryType != nil {
			memoryType := domainmemory.MemoryType(trimSpace(*body.MemoryType))
			in.Type = &memoryType
		}
		view, err := svc.UpdateMemory(ctx, in)
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		c.JSON(http.StatusOK, view)
	}
}

// DeleteMemoryHandler answers DELETE /v1/memories/:memory_id.
//
// The id is scoped by the caller's user id inside the service, so one user
// deleting another user's memory id surfaces not_found rather than success.
func DeleteMemoryHandler(svc ChatService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		userID := userIDFromContext(ctx)
		if userID == "" {
			WriteAndAbort(ctx, c, errs.New(errs.CodeInvalidArgument,
				HeaderUserID+" header is required to delete a memory"))
			return
		}
		memoryID := c.Param("memory_id")
		if memoryID == "" {
			WriteAndAbort(ctx, c, errs.New(errs.CodeInvalidArgument, "memory_id is required"))
			return
		}
		if err := svc.DeleteMemory(ctx, userID, memoryID); err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}
