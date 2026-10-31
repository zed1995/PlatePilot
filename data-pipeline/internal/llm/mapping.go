package llm

import "github.com/zed1995/platepilot/shared/domain/chat"

// toWireMessages maps project messages onto the protocol shape. Every domain
// role already matches the protocol, but going through an explicit mapping
// keeps vendor-neutral DTOs out of the (un)marshal path.
func toWireMessages(in []chat.ChatMessage) []wireMessage {
	out := make([]wireMessage, 0, len(in))
	for _, m := range in {
		out = append(out, wireMessage{
			Role:    string(m.Role),
			Content: m.Content,
		})
	}
	return out
}

// toDomainMessage maps a response message back to the domain DTO.
func toDomainMessage(wm wireMessage) chat.ChatMessage {
	return chat.ChatMessage{
		Role:    chat.Role(wm.Role),
		Content: wm.Content,
	}
}

func toUsage(u completionUsage) chat.TokenUsage {
	total := u.TotalTokens
	if total == 0 {
		total = u.PromptTokens + u.CompletionTokens
	}
	return chat.TokenUsage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		TotalTokens:  total,
	}
}

// mapFinishReason passes the protocol reason through. The domain type is a
// string so unknown future values survive instead of being blanked out.
func mapFinishReason(reason string) chat.FinishReason {
	return chat.FinishReason(reason)
}
