package llm

// chatCompletionsPath is the only endpoint the adapter talks to.
const chatCompletionsPath = "/chat/completions"

// ---- Request wire types ---------------------------------------------------

// completionRequest is the /chat/completions request body. Fields use the
// upstream protocol names; they are deliberately unexported so no caller can
// build a vendor payload outside the package.
type completionRequest struct {
	Model       string        `json:"model"`
	Messages    []wireMessage `json:"messages"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

// wireMessage is one chat message on the wire.
type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ---- Response wire types --------------------------------------------------

// completionResponse is the non-streaming response body.
type completionResponse struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   completionUsage    `json:"usage"`
	// Error is a failure the gateway reports with HTTP 200 and no choices
	// (OpenRouter does this when the routed upstream is overloaded). It is a
	// pointer so "absent" stays distinguishable from an empty envelope.
	Error *apiErrorBody `json:"error,omitempty"`
}

type completionChoice struct {
	Index        int         `json:"index"`
	Message      wireMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type completionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// apiError is the OpenAI-family error envelope returned with non-2xx statuses.
type apiError struct {
	Error apiErrorBody `json:"error"`
}

type apiErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	// Code is either a string (OpenAI) or a number (OpenRouter gateways);
	// kept opaque since it never drives control flow.
	Code  any    `json:"code"`
	Param string `json:"param"`
}
