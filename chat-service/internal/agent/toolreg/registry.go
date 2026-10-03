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

	"github.com/zed1995/platepilot/shared/domain/errs"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
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

// Confirmation states how a tool's side effects are authorised.
//
// It is a required declaration rather than a bool because a bool cannot tell
// "this write needs the user's approval" from "the author never thought about
// it". Both are the zero value, so a tool that writes and a tool nobody
// considered would register identically — and the consequence of the second is
// a write that nothing authorised. Making the declaration a separate string
// leaves the omitted case as the empty one, which Register can refuse.
type Confirmation string

const (
	// ConfirmationNone is the declaration of a tool with no side effects. It is
	// optional for a read-only spec and refused on a writing one.
	ConfirmationNone Confirmation = "none"
	// ConfirmationRequired parks a call instead of running it: the tool is
	// recorded as the thread's pending action and executes only once the user
	// confirms that exact call. This is the policy for anything that changes the
	// world outside the conversation.
	ConfirmationRequired Confirmation = "required"
	// ConfirmationImplicit lets a tool write without a confirmation step, for
	// the narrow case where the user's own message is the authorisation:
	// "记住我不吃辣" is an instruction the tool carries out, not a request it has
	// to have approved. Declaring it is a deliberate act, which is the point.
	ConfirmationImplicit Confirmation = "implicit"
)

// validate checks the declaration against what the spec says about side effects.
//
// The pairing is enforced in both directions. A writing tool without a policy
// is the case this exists for. A read-only tool that claims it needs
// confirmation is also refused, because the gate would park a call that cannot
// change anything — turning a lookup into a question the user has to answer.
func (c Confirmation) validate(name string, readOnly bool) error {
	switch c {
	case "":
		if readOnly {
			// A read-only tool's side-effect story is already complete.
			return nil
		}
		return fmt.Errorf("toolreg: tool %q is not read-only but declares no confirmation policy; "+
			"set Confirmation to %q or %q", name, ConfirmationRequired, ConfirmationImplicit)
	case ConfirmationNone:
		if !readOnly {
			return fmt.Errorf("toolreg: tool %q is not read-only but declares ConfirmationNone; "+
				"set Confirmation to %q or %q", name, ConfirmationRequired, ConfirmationImplicit)
		}
		return nil
	case ConfirmationRequired, ConfirmationImplicit:
		if readOnly {
			return fmt.Errorf("toolreg: tool %q is read-only but declares confirmation %q; "+
				"a call with no side effects has nothing to authorise", name, c)
		}
		return nil
	default:
		return fmt.Errorf("toolreg: tool %q has unknown confirmation policy %q "+
			"(want %q, %q or %q)", name, c, ConfirmationNone, ConfirmationRequired, ConfirmationImplicit)
	}
}

// Entry registers one tool.
type Entry struct {
	Spec    domaintool.ToolSpec
	Handler Handler
	// Timeout overrides the registry default; Spec.TimeoutMS is consulted when
	// this is zero.
	Timeout time.Duration
	// Confirmation declares how this tool's side effects are authorised. It is
	// mandatory for every tool whose Spec.ReadOnly is false; see Confirmation.
	Confirmation Confirmation
	// SummarizeApproval renders the description of a call that is about to be
	// parked for the user's approval. It is required when Confirmation is
	// ConfirmationRequired and refused otherwise.
	//
	// The summary is produced by the tool rather than by the gate because only
	// the tool knows what its arguments mean. "2 人 · Joe's Pizza · 2026-10-10
	// 19:00（政策 mock-v1）" is a sentence about a reservation; a generic
	// renderer could only ever hand the user a JSON blob and ask them to
	// approve it, which is not an approval anyone can give.
	SummarizeApproval func(ctx context.Context, args json.RawMessage) (string, error)
}

// RequiresConfirmation reports whether a call to this tool must be parked for
// the user's approval before it runs.
func (e Entry) RequiresConfirmation() bool {
	return e.Confirmation == ConfirmationRequired
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
// handler, a duplicate name, a parameter schema that is not a valid JSON object
// schema, or a confirmation policy that does not match the spec's read-only
// flag.
func (r *Registry) Register(entry Entry) error {
	if entry.Handler == nil {
		return fmt.Errorf("toolreg: tool %q handler is nil", entry.Spec.Name)
	}
	if !namePattern.MatchString(entry.Spec.Name) {
		return fmt.Errorf("toolreg: tool name %q must match %s", entry.Spec.Name, namePattern.String())
	}
	if err := entry.Confirmation.validate(entry.Spec.Name, entry.Spec.ReadOnly); err != nil {
		return err
	}
	// A parked call has to be described to the user, so the renderer is part of
	// the contract rather than an optional nicety: without it the gate would
	// have to approve a call by printing its raw arguments.
	if entry.RequiresConfirmation() && entry.SummarizeApproval == nil {
		return fmt.Errorf("toolreg: tool %q requires confirmation but has no SummarizeApproval",
			entry.Spec.Name)
	}
	if !entry.RequiresConfirmation() && entry.SummarizeApproval != nil {
		return fmt.Errorf("toolreg: tool %q declares SummarizeApproval but never parks a call for "+
			"confirmation", entry.Spec.Name)
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

// RequiresConfirmation reports whether a registered tool must be parked for the
// user's approval. An unknown tool reports false: it cannot be executed either,
// so a caller that only gates known tools is not skipping a write.
//
// It is a lookup rather than a field on the tool spec because the policy is a
// server-side authorisation decision. The spec is what the model is shown, and
// telling the model which of its tools a human will review invites it to treat
// that as a negotiation step it can argue with.
func (r *Registry) RequiresConfirmation(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, found := r.entries[name]
	return found && reg.entry.RequiresConfirmation()
}

// ApprovalSummary renders the description of a call that is about to be parked.
//
// The renderer is invoked outside the tool's own timeout: it is a presentation
// step, not the work being approved, and a summary that failed because the
// store was briefly slow would tell the user their request was malformed.
func (r *Registry) ApprovalSummary(
	ctx context.Context, name string, args json.RawMessage,
) (string, error) {
	r.mu.RLock()
	reg, found := r.entries[name]
	r.mu.RUnlock()
	if !found {
		return "", fmt.Errorf("toolreg: unknown tool %q", name)
	}
	if reg.entry.SummarizeApproval == nil {
		return "", fmt.Errorf("toolreg: tool %q has no approval summary", name)
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	return reg.entry.SummarizeApproval(ctx, args)
}
