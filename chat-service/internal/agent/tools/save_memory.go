package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/memorywrite"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domainmemory "github.com/zed1995/platepilot/shared/domain/memory"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
)

// SaveMemoryToolName is the tool name the model sees.
const SaveMemoryToolName = "save_memory"

// maxContentRunesForSchema mirrors memorywrite.MaxContentRunes in the schema the
// model reads. The real limit is enforced in the handler; repeating it here is
// what lets a model see the bound before it trips over it.
const maxContentRunesForSchema = memorywrite.MaxContentRunes

var saveMemorySchema = fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "content": {
      "type": "string",
      "maxLength": %d,
      "description": "The memory to keep, written as a short statement about the user, e.g. \"不吃辣\""
    },
    "memory_type": {
      "type": "string",
      "enum": ["preference", "constraint", "fact"],
      "description": "constraint for a hard requirement, preference for a default, fact for background"
    },
    "quoted_user_text": {
      "type": "string",
      "description": "Verbatim fragment of the user's own message that asks for this memory. Required. A memory that the user did not ask for is refused."
    }
  },
  "required": ["content", "memory_type", "quoted_user_text"],
  "additionalProperties": false
}`, maxContentRunesForSchema)

type saveMemoryArgs struct {
	Content        string `json:"content"`
	MemoryType     string `json:"memory_type"`
	QuotedUserText string `json:"quoted_user_text"`
}

// SavedMemory is the tool's payload, and the shape the memory.saved event is
// built from. It is a projection of the stored row rather than the row itself:
// the user id and the embedding are not the model's business and not the
// client's either.
type SavedMemory struct {
	MemoryID   string `json:"memory_id"`
	MemoryType string `json:"memory_type"`
	Content    string `json:"content"`
	// Refreshed reports that an identical memory already existed and its
	// timestamp moved forward, so the answer can say "还是记着" rather than
	// claiming a new one.
	Refreshed bool `json:"refreshed"`
}

// SaveMemoryEntry builds the memory-writing tool.
//
// It declares ConfirmationImplicit rather than ConfirmationRequired, and that is
// the one case the policy exists for: the user's own message is the
// authorisation. "记住我不吃辣" is an instruction, not a request for approval —
// asking "shall I remember that?" after being told to remember it would be a
// question the user already answered.
//
// The declaration is not a hole in the gate. What stands in for the approval is
// a check the model cannot satisfy by wanting to: it must quote the user's own
// words, and the quote is verified against the turn's message. A model that
// decided on its own that a preference was worth keeping has nothing to quote.
func SaveMemoryEntry(svc *memorywrite.Service) toolreg.Entry {
	return toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name: SaveMemoryToolName,
			Description: "Remember something about the user for future conversations. " +
				"Call it only when the user asks you to remember something, and quote the " +
				"part of their message that asks for it. Never call it on your own initiative.",
			Parameters: json.RawMessage(saveMemorySchema),
			ReadOnly:   false,
		},
		Confirmation: toolreg.ConfirmationImplicit,
		Handler: func(ctx context.Context, raw json.RawMessage) (domaintool.ToolResult, error) {
			var args saveMemoryArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return domaintool.ToolResult{}, errs.Wrap(errs.CodeValidationFailed,
					"decode save_memory arguments", err)
			}
			result, err := svc.Save(ctx, memorywrite.Request{
				Content:        args.Content,
				Type:           domainmemory.MemoryType(args.MemoryType),
				QuotedUserText: args.QuotedUserText,
			})
			if err != nil {
				return domaintool.ToolResult{}, err
			}
			payload, err := json.Marshal(SavedMemory{
				MemoryID:   result.Memory.ID,
				MemoryType: string(result.Memory.Type),
				Content:    result.Memory.Content,
				Refreshed:  result.Refreshed,
			})
			if err != nil {
				return domaintool.ToolResult{}, errs.Wrap(errs.CodeInternal,
					"encode saved memory", err)
			}
			return domaintool.ToolResult{
				Status:  domaintool.ToolStatusOK,
				Content: renderSavedMemory(result.Memory, result.Refreshed),
				Data:    payload,
			}, nil
		},
	}
}

// renderSavedMemory states what was kept in the user's own terms.
//
// The text is what the answer is built from, so it says "已记住" rather than
// reporting an id: the user asked for something to be remembered, and the
// confirmation of that is the sentence, not a row key.
func renderSavedMemory(mem domainmemory.Memory, refreshed bool) string {
	if refreshed {
		return fmt.Sprintf("已经记着了：%s（本次未重复记录）", mem.Content)
	}
	return fmt.Sprintf("已记住：%s", mem.Content)
}
