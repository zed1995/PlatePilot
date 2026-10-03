package httpapi

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/zed/platepilot/chat-service/internal/httperr"
	"github.com/zed/platepilot/shared/domain/errs"
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
