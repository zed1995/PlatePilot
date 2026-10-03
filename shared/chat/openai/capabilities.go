package openai

// Capabilities declares what one configured chat model can do.
//
// Model behaviour on OpenAI-compatible gateways is heterogeneous: whether
// function calling, parallel calls, strict JSON Schema, or long contexts are
// supported is a property of the model behind the endpoint, not of the
// protocol. Declaring it in configuration lets the agent reject an impossible
// request locally instead of sending it and receiving prose where it expected
// a tool call.
type Capabilities struct {
	// Tools enables function/tool calling.
	Tools bool
	// ParallelTools allows one turn to request several tool calls. Many
	// smaller models misbehave when this is enabled, so deployments opt in.
	ParallelTools bool
	// JSONSchema selects strict response_format json_schema. When false the
	// adapter falls back to json_object plus an in-prompt schema.
	JSONSchema bool
	// Streaming is declared because some hosted models only serve unary calls.
	Streaming bool
	// ContextTokens is the model context window. The agent trims history
	// against it (M5); zero is rejected at construction time.
	ContextTokens int
}

// DefaultCapabilities are the conservative M4 defaults: tools and streaming
// are on (the milestone cannot function without either), while parallel
// calling and strict schema are opt-in.
func DefaultCapabilities() Capabilities {
	return Capabilities{
		Tools:         true,
		ParallelTools: false,
		JSONSchema:    false,
		Streaming:     true,
		ContextTokens: 32768,
	}
}
