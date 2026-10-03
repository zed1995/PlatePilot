// Package toolreg is the agent's tool registry.
//
// It owns the contract between the model and project-side tools: tool specs are
// validated once at registration, and every invocation is funnelled through
// Invoke, which converts unknown tools, malformed arguments, timeouts, and
// handler panics into structured ToolResult values instead of letting them
// tear down the reasoning loop. This is one of the two packages allowed to name
// Eino types; EinoTool bridges an entry onto Eino's tool.InvokableTool.
package toolreg

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
	domaintool "github.com/zed/platepilot/shared/domain/tool"
)

// namePattern pins tool names to lowercase snake-case identifiers that every
// vendor wire format accepts. Length is bounded so the name cannot be used to
// blow up provider requests.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// DefaultTimeout is used when neither the entry nor the spec names one.
const DefaultTimeout = 15 * time.Second

// Handler executes one tool call. Args are the raw JSON the model produced. The
// registry fills CallID and Name on the returned ToolResult, so handlers only
// populate Status/Content/Data/Error.
type Handler func(ctx context.Context, args json.RawMessage) (domaintool.ToolResult, error)

// Entry registers one tool.
type Entry struct {
	Spec    domaintool.ToolSpec
	Handler Handler
	// Timeout overrides the registry default; Spec.TimeoutMS is consulted when
	// this is zero.
	Timeout time.Duration
}

// registered is an entry plus its pre-parsed parameter schema.
type registered struct {
	entry  Entry
	schema *schemaNode
}

// New builds an empty registry with the given default timeout. A non-positive
// default falls back to DefaultTimeout.
func New(defaultTimeout time.Duration) *Registry {
	if defaultTimeout <= 0 {
		defaultTimeout = DefaultTimeout
	}
	return &Registry{
		entries:        make(map[string]registered),
		defaultTimeout: defaultTimeout,
	}
}

// Registry holds the registered tools.
type Registry struct {
	mu             sync.RWMutex
	entries        map[string]registered
	defaultTimeout time.Duration
}

// Register validates and stores one entry. It fails on a malformed name, a nil
// handler, a duplicate name, or a parameter schema that is not a valid JSON
// object schema.
func (r *Registry) Register(entry Entry) error {
	if entry.Handler == nil {
		return fmt.Errorf("toolreg: tool %q handler is nil", entry.Spec.Name)
	}
	if !namePattern.MatchString(entry.Spec.Name) {
		return fmt.Errorf("toolreg: tool name %q must match %s", entry.Spec.Name, namePattern.String())
	}
	schemaNode, err := parseSchema(entry.Spec.Parameters)
	if err != nil {
		return fmt.Errorf("toolreg: tool %q: %w", entry.Spec.Name, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[entry.Spec.Name]; exists {
		return fmt.Errorf("toolreg: tool %q is already registered", entry.Spec.Name)
	}
	if entry.Timeout <= 0 {
		if entry.Spec.TimeoutMS > 0 {
			entry.Timeout = time.Duration(entry.Spec.TimeoutMS) * time.Millisecond
		} else {
			entry.Timeout = r.defaultTimeout
		}
	}
	r.entries[entry.Spec.Name] = registered{entry: entry, schema: schemaNode}
	return nil
}

// Specs returns the registered tool specs in name order, ready to pass to
// ChatWithTools.
func (r *Registry) Specs() []domaintool.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]domaintool.ToolSpec, 0, len(names))
	for _, name := range names {
		reg := r.entries[name]
		spec := reg.entry.Spec
		spec.TimeoutMS = int(reg.entry.Timeout / time.Millisecond)
		specs = append(specs, spec)
	}
	return specs
}

// Invoke runs one tool call. It never panics and always returns a ToolResult:
// unknown tools, invalid arguments, timeouts, and handler panics are all
// represented as error results carrying a stable errs.Code.
func (r *Registry) Invoke(ctx context.Context, call domaintool.ToolCall) (result domaintool.ToolResult) {
	result = domaintool.ToolResult{CallID: call.ID, Name: call.Name, Status: domaintool.ToolStatusError}

	r.mu.RLock()
	reg, found := r.entries[call.Name]
	r.mu.RUnlock()
	if !found {
		result.Error = errs.Newf(errs.CodeNotFound, "unknown tool %q", call.Name)
		return result
	}

	args := call.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var decoded any
	if err := json.Unmarshal(args, &decoded); err != nil {
		result.Error = errs.Wrap(errs.CodeValidationFailed,
			fmt.Sprintf("tool %q received arguments that are not valid JSON", call.Name), err)
		return result
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		result.Error = errs.Newf(errs.CodeValidationFailed,
			"tool %q expects a JSON object of arguments", call.Name)
		return result
	}
	if err := reg.schema.validate(obj, "$"); err != nil {
		result.Error = errs.Newf(errs.CodeValidationFailed, "tool %q: %v", call.Name, err)
		return result
	}

	callCtx, cancel := context.WithTimeout(ctx, reg.entry.Timeout)
	defer cancel()

	defer func() {
		if recovered := recover(); recovered != nil {
			result.Status = domaintool.ToolStatusError
			result.Content = ""
			result.Data = nil
			result.Error = errs.Newf(errs.CodeInternal,
				"tool %q panicked: %v", call.Name, recovered)
		}
	}()

	out, err := reg.entry.Handler(callCtx, args)
	if callCtx.Err() == context.DeadlineExceeded {
		result.Status = domaintool.ToolStatusError
		result.Error = errs.Newf(errs.CodeProviderTimeout,
			"tool %q timed out after %s", call.Name, reg.entry.Timeout)
		return result
	}
	if err != nil {
		result.Status = domaintool.ToolStatusError
		if e, isDomainErr := errs.As(err); isDomainErr {
			result.Error = e
		} else {
			result.Error = errs.Wrap(errs.CodeInternal,
				fmt.Sprintf("tool %q failed", call.Name), err)
		}
		return result
	}
	out.CallID = call.ID
	out.Name = call.Name
	if out.Status == "" {
		out.Status = domaintool.ToolStatusOK
	}
	return out
}

// TimeoutOf exposes the effective timeout of a registered tool so the graph can
// report it on tool.finish events.
func (r *Registry) TimeoutOf(name string) (time.Duration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, found := r.entries[name]
	return reg.entry.Timeout, found
}
