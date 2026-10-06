// Package openai implements the project chat ports against an OpenAI-compatible
// /chat/completions endpoint.
//
// OpenRouter is one configuration of this adapter, not a dependency of the
// domain: the base URL, API key, and model id all arrive through Options, and
// the vendor wire types in this file never leave the package. The same adapter
// also works against vLLM, Ollama's compatibility route, or any other service
// speaking the same protocol.
package openai

import (
	"encoding/json"
	"fmt"
)

// chatCompletionsPath is the only endpoint the adapter talks to.
const chatCompletionsPath = "/chat/completions"

// ---- Request wire types ---------------------------------------------------

// completionRequest is the /chat/completions request body. Fields use the
// upstream protocol names; they are deliberately unexported so no caller can
// build a vendor payload outside the package.
type completionRequest struct {
	Model             string          `json:"model"`
	Messages          []wireMessage   `json:"messages"`
	Tools             []wireTool      `json:"tools,omitempty"`
	ToolChoice        string          `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	MaxTokens         int             `json:"max_tokens,omitempty"`
	Stream            bool            `json:"stream,omitempty"`
	ResponseFormat    *responseFormat `json:"response_format,omitempty"`
}

// responseFormat selects structured output. Type is "json_object" (broadly
// supported) or "json_schema" (strict, model-dependent).
type responseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *jsonSchemaSpec `json:"json_schema,omitempty"`
}

// jsonSchemaSpec carries the strict structured-output envelope.
type jsonSchemaSpec struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

// wireMessage is one chat message on the wire. Tool messages additionally
// carry ToolCallID; assistant messages may carry ToolCalls.
type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
}

// wireTool is the function-tool envelope offered to the model.
type wireTool struct {
	Type     string           `json:"type"`
	Function wireToolFunction `json:"function"`
}

// wireToolFunction describes a callable function; Parameters is a JSON Schema.
type wireToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// wireToolCall appears inside an assistant message. On the OpenAI protocol
// Function.Arguments is a JSON-encoded *string* whose content is JSON, which
// is why functionArguments has custom (un)marshalling rather than embedding
// json.RawMessage directly.
type wireToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function wireFunctionCall `json:"function"`
}

// wireFunctionCall is the function invocation named inside a tool call.
type wireFunctionCall struct {
	Name      string            `json:"name,omitempty"`
	Arguments functionArguments `json:"arguments,omitempty"`
}

// functionArguments keeps the raw JSON the model passed while (un)marshalling
// the string envelope the protocol requires.
type functionArguments struct {
	Raw json.RawMessage
}

// MarshalJSON encodes the inner JSON as a JSON string.
func (a functionArguments) MarshalJSON() ([]byte, error) {
	if len(a.Raw) == 0 {
		return []byte(`""`), nil
	}
	// json.Marshal of a string applies the required escaping.
	return json.Marshal(string(a.Raw))
}

// UnmarshalJSON accepts the protocol's string form. The string content is kept
// verbatim; the caller validates that it is usable JSON.
func (a *functionArguments) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("tool arguments must be a JSON string: %w", err)
	}
	a.Raw = json.RawMessage(s)
	return nil
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

// apiError is the OpenAI-family error envelope returned with non-2xx statuses
// and inside SSE error frames.
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

// ---- Streaming wire types -------------------------------------------------

// completionChunk is one SSE data frame.
type completionChunk struct {
	ID      string            `json:"id"`
	Model   string            `json:"model"`
	Choices []completionDelta `json:"choices"`
	Usage   *completionUsage  `json:"usage,omitempty"`
}

type completionDelta struct {
	Index        int       `json:"index"`
	Delta        wireDelta `json:"delta"`
	FinishReason string    `json:"finish_reason"`
}

type wireDelta struct {
	Role      string              `json:"role,omitempty"`
	Content   string              `json:"content,omitempty"`
	ToolCalls []wireToolCallDelta `json:"tool_calls,omitempty"`
	// Reasoning is the chain-of-thought delta some reasoning models stream
	// before (or alongside) Content. ReasoningContent is the same idea under
	// the name DeepSeek-style providers use; both are mapped so one adapter
	// covers either dialect.
	Reasoning        string `json:"reasoning,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// wireToolCallDelta carries one fragment of a tool call. The first fragment
// for an index usually carries ID/Type/Function.Name; later fragments append
// to Function.Arguments.
type wireToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}
