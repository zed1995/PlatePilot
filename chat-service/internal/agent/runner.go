package agent

import (
	"context"
	"time"

	chatport "github.com/zed1995/platepilot/shared/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/idgen"
	"github.com/zed1995/platepilot/shared/store"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"

	"github.com/zed1995/platepilot/chat-service/internal/agent/answer"
	"github.com/zed1995/platepilot/chat-service/internal/agent/audit"
	"github.com/zed1995/platepilot/chat-service/internal/agent/einomodel"
	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
)

// Defaults for the reasoning loop knobs.
const (
	defaultMaxToolRounds = 5
	// defaultMaxClarifications bounds how many times one thread may ask the
	// user to disambiguate before the turn has to proceed on a stated
	// assumption. Three is enough for a genuinely confusing name and far short
	// of a loop: a fourth question would mean the first three changed nothing.
	defaultMaxClarifications = 3
	eventBuffer              = 128
)

// Config holds the runner knobs.
type Config struct {
	// MaxToolRounds bounds how many tool-execution rounds one turn may run.
	MaxToolRounds int
	// ModelName optionally overrides the provider's default model.
	ModelName string
	// MaxClarifications bounds consecutive disambiguation rounds per thread.
	MaxClarifications int
}

// Deps are the assembled capabilities a runner needs.
type Deps struct {
	Chat        chatport.ChatProvider
	ToolCalling chatport.ToolCallingProvider
	Registry    *toolreg.Registry
	// Composer may be nil; it is then built from Chat.
	Composer *answer.Composer
	// Extractor interprets the user's message into this turn's plan. When nil,
	// the turn runs without one and every tool works purely from the arguments
	// the model produced — which is a supported deployment, not a broken one,
	// but it does mean a condition the model forgets to pass through is not
	// recovered.
	Extractor *slots.Extractor
	// Auditor records run/tool-call audit rows. When nil, the turn runs
	// without the audit side path.
	Auditor *audit.Hooks
	// Conversations, when set, enables history replay, transcript
	// persistence, and checkpoints.
	Conversations store.ConversationRepository
	// Memories, when set, injects the user's long-term memories at ingress.
	Memories store.MemoryRepository
}

// NewRunner assembles and compiles the turn graph.
func NewRunner(cfg Config, deps Deps) (*Runner, error) {
	if deps.Chat == nil {
		return nil, errs.New(errs.CodeInvalidArgument, "agent runner requires a Chat provider")
	}
	if deps.Registry == nil {
		deps.Registry = toolreg.New(toolreg.DefaultTimeout)
	}
	if cfg.MaxToolRounds <= 0 {
		cfg.MaxToolRounds = defaultMaxToolRounds
	}
	if cfg.MaxClarifications <= 0 {
		cfg.MaxClarifications = defaultMaxClarifications
	}
	baseModel, err := einomodel.New(einomodel.Deps{
		Chat:        deps.Chat,
		ToolCalling: deps.ToolCalling,
		Model:       cfg.ModelName,
	})
	if err != nil {
		return nil, err
	}
	toolInfos, err := deps.Registry.ToolInfos()
	if err != nil {
		return nil, err
	}
	planModel, err := baseModel.WithTools(toolInfos)
	if err != nil {
		return nil, err
	}
	composer := deps.Composer
	if composer == nil {
		composer = answer.NewComposer(answer.Deps{Chat: deps.Chat, Model: cfg.ModelName})
	}

	r := &Runner{
		cfg:         cfg,
		deps:        deps,
		planModel:   planModel,
		composer:    composer,
		hasTools:    len(toolInfos) > 0,
		toolSupport: deps.ToolCalling != nil && deps.ToolCalling.SupportsTools(),
	}
	if err := r.compile(); err != nil {
		return nil, err
	}
	return r, nil
}

// Runner executes conversation turns through the compiled graph.
type Runner struct {
	cfg       Config
	deps      Deps
	planModel model.ToolCallingChatModel
	composer  *answer.Composer

	runnable    compose.Runnable[TurnInput, *TurnResult]
	hasTools    bool
	toolSupport bool
}

// emitter publishes turn events onto a buffered channel. Sends respect the run
// context so a consumer that goes away never wedges a node.
type emitter struct {
	ch chan Event
}

func newEmitter() *emitter {
	return &emitter{ch: make(chan Event, eventBuffer)}
}

func (e *emitter) send(ctx context.Context, ev Event) {
	select {
	case e.ch <- ev:
	case <-ctx.Done():
	}
}

// Run executes one turn. It blocks until the turn finishes; the returned
// channel streams run events and is always closed before Run returns. A nil
// result pairs with an error event describing why the turn failed.
func (r *Runner) Run(ctx context.Context, in TurnInput) (*TurnResult, <-chan Event) {
	em := newEmitter()
	events := em.ch

	if in.UserInput == "" {
		r.fail(ctx, em, in, runMeta{}, errs.New(errs.CodeInvalidArgument, "user input must not be empty"))
		return nil, events
	}
	if r.hasTools && !r.toolSupport {
		r.fail(ctx, em, in, runMeta{}, errs.New(errs.CodeInvalidArgument,
			"tools are registered but the configured chat provider does not support tool calling"))
		return nil, events
	}

	traceID := in.TraceID
	if traceID == "" {
		traceID = idgen.NewUUID()
	}
	meta := runMeta{
		runID:     idgen.NewUUID(),
		traceID:   traceID,
		threadID:  in.ThreadID,
		startedAt: time.Now(),
	}

	runCtx := withEmitter(ctx, em)
	runCtx = withRunMeta(runCtx, meta)
	r.deps.Auditor.RunStart(runCtx, audit.Meta{
		RunID: meta.runID, TraceID: meta.traceID,
		ThreadID: meta.threadID, StartedAt: meta.startedAt,
	})

	result, err := r.runnable.Invoke(runCtx, in)
	if err != nil {
		r.deps.Auditor.Fail(runCtx, audit.Meta{
			RunID: meta.runID, TraceID: meta.traceID,
			ThreadID: meta.threadID, StartedAt: meta.startedAt,
		}, err)
		r.fail(runCtx, em, in, meta, err)
		return nil, events
	}
	em.send(runCtx, Event{
		Type:         EventEnd,
		RunID:        result.RunID,
		ThreadID:     result.ThreadID,
		FinishReason: string(result.FinishReason),
		Usage:        &result.Usage,
		Warnings:     result.Warnings,
	})
	close(events)
	return result, events
}

// fail emits the terminal error event and closes the event channel. A
// background context is used for the final send so cancellation of the run
// context cannot suppress the error the consumer is draining the channel for.
func (r *Runner) fail(_ context.Context, em *emitter, in TurnInput, meta runMeta, err error) {
	code := errs.CodeOf(err)
	ev := Event{
		Type:     EventError,
		ThreadID: in.ThreadID,
		Code:     string(code),
		Message:  err.Error(),
	}
	if meta.runID != "" {
		ev.RunID = meta.runID
	}
	em.send(context.Background(), ev)
	close(em.ch)
}

// runMeta is the per-run identity minted in Run and read by the graph nodes.
type runMeta struct {
	runID     string
	traceID   string
	threadID  string
	startedAt time.Time
}

type runMetaKey struct{}

func withRunMeta(ctx context.Context, meta runMeta) context.Context {
	return context.WithValue(ctx, runMetaKey{}, meta)
}

func runMetaFromContext(ctx context.Context) runMeta {
	if meta, ok := ctx.Value(runMetaKey{}).(runMeta); ok {
		return meta
	}
	return runMeta{}
}

// compile wires the Eino state machine:
//
//	START -> ingress -> plan -+-> tools -> plan (loop, bounded)
//	                          +-> clarify -> finalize -> END
//	                          +-> answer -> finalize -> END
//
// A branch leaves plan for three destinations, and the order of the tests is
// the priority: a turn that has to ask the user something asks before it does
// anything else, because running more tools against a restaurant the user has
// not chosen yet spends budget to answer about the wrong place.
func (r *Runner) compile() error {
	g := compose.NewGraph[TurnInput, *TurnResult]()

	if err := g.AddLambdaNode(nodeIngress, compose.InvokableLambda(r.ingress)); err != nil {
		return err
	}
	if err := g.AddLambdaNode(nodePlan, compose.InvokableLambda(r.plan)); err != nil {
		return err
	}
	if err := g.AddLambdaNode(nodeTools, compose.InvokableLambda(r.runTools)); err != nil {
		return err
	}
	if err := g.AddLambdaNode(nodeClarify, compose.InvokableLambda(r.clarifyNode)); err != nil {
		return err
	}
	if err := g.AddLambdaNode(nodeAnswer, compose.InvokableLambda(r.answerNode)); err != nil {
		return err
	}
	if err := g.AddLambdaNode(nodeFinalize, compose.InvokableLambda(r.finalize)); err != nil {
		return err
	}

	if err := g.AddEdge(compose.START, nodeIngress); err != nil {
		return err
	}
	if err := g.AddEdge(nodeIngress, nodePlan); err != nil {
		return err
	}
	if err := g.AddEdge(nodeTools, nodePlan); err != nil {
		return err
	}
	// The clarifying turn is terminal: it emits its question and the checkpoint
	// that keeps the thread waiting. It deliberately does not pass through
	// answer, which would either overwrite the question or — worse — compose a
	// recommendation about a restaurant the user has not chosen.
	if err := g.AddEdge(nodeClarify, nodeFinalize); err != nil {
		return err
	}
	if err := g.AddEdge(nodeAnswer, nodeFinalize); err != nil {
		return err
	}
	if err := g.AddEdge(nodeFinalize, compose.END); err != nil {
		return err
	}

	branch := compose.NewGraphBranch[*TurnState](
		func(_ context.Context, st *TurnState) (string, error) {
			if st.needsClarification() {
				return nodeClarify, nil
			}
			if st.PendingToolCalls {
				return nodeTools, nil
			}
			return nodeAnswer, nil
		},
		map[string]bool{nodeTools: true, nodeClarify: true, nodeAnswer: true},
	)
	if err := g.AddBranch(nodePlan, branch); err != nil {
		return err
	}

	runnable, err := g.Compile(context.Background())
	if err != nil {
		return err
	}
	r.runnable = runnable
	return nil
}

// node keys
const (
	nodeIngress  = "ingress"
	nodePlan     = "plan"
	nodeTools    = "tools"
	nodeClarify  = "clarify"
	nodeAnswer   = "answer"
	nodeFinalize = "finalize"
)
