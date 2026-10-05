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
	// ModelName optionally overrides the provider's default model for the
	// planning rounds — the model that decides what to do next.
	ModelName string
	// AnswerModel optionally overrides the provider's default model for
	// composing the final answer. Empty means "the same as ModelName": the two
	// jobs may share a model, and a caller that names only one should not have
	// to name it twice.
	AnswerModel string
	// MaxClarifications bounds consecutive disambiguation rounds per thread.
	MaxClarifications int
	// AnswerStreaming publishes the composed answer token by token instead of
	// in one piece. The zero value is off, so a caller that has not thought
	// about it gets the older, single-delta behaviour; the application turns it
	// on from configuration.
	AnswerStreaming bool
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
		answerModel := cfg.AnswerModel
		if answerModel == "" {
			answerModel = cfg.ModelName
		}
		composer = answer.NewComposer(answer.Deps{Chat: deps.Chat, Model: answerModel})
	}

	r := &Runner{
		cfg:          cfg,
		deps:         deps,
		planModel:    planModel,
		composer:     composer,
		streamAnswer: cfg.AnswerStreaming,
		hasTools:     len(toolInfos) > 0,
		toolSupport:  deps.ToolCalling != nil && deps.ToolCalling.SupportsTools(),
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
	// streamAnswer mirrors cfg.AnswerStreaming, resolved once so the answer
	// node reads a field instead of re-deriving the decision every turn.
	streamAnswer bool

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

// Run executes one turn to completion and then hands back everything it
// published. The returned channel streams run events and is always closed before
// Run returns, which is what makes it a batch interface: every frame is
// available at the end, none of them before. A nil result pairs with an error
// event describing why the turn failed.
//
// A caller that renders the answer as it is written — the SSE handler — cannot
// use this one; it wants RunLive.
func (r *Runner) Run(ctx context.Context, in TurnInput) (*TurnResult, <-chan Event) {
	em := newEmitter()
	result, _ := r.invoke(ctx, em, in)
	close(em.ch)
	return result, em.ch
}

// RunLive executes one turn and hands each event to onEvent as the graph
// produces it, returning when the turn is over.
//
// The two entry points exist because they answer different questions. Run
// answers "what did the turn produce"; RunLive answers "and when did each part
// of it happen". Only the second can serve a streaming transport: a delta
// delivered after the turn has finished is not a first token, it is the whole
// answer in pieces, and that is exactly the difference streaming is supposed to
// make.
//
// Draining concurrently with the graph is also what keeps the emitter's buffer
// from becoming a wall. The channel holds eventBuffer frames; a turn that
// publishes more than that while nobody reads would block on the next send
// until the client's context expired, so a long answer could not be delivered
// at all.
//
// An error from onEvent stops the turn — the client is gone, and the run is
// cancelled the way a closed connection should cancel it — and is returned
// after the stream has drained. The turn's own failure is not returned: it has
// already been published as an error event, which is the form the transport
// needs and the form every other caller reads.
func (r *Runner) RunLive(
	ctx context.Context, in TurnInput, onEvent func(Event) error,
) (*TurnResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	em := newEmitter()
	finished := make(chan *TurnResult, 1)
	go func() {
		defer close(em.ch)
		result, _ := r.invoke(ctx, em, in)
		finished <- result
	}()

	var forwardErr error
	for ev := range em.ch {
		if forwardErr != nil {
			// Keep draining rather than returning early: the graph must never
			// block on a buffer nobody is reading, and it stops on its own once
			// the cancelled context reaches it.
			continue
		}
		if err := onEvent(ev); err != nil {
			forwardErr = err
			cancel()
		}
	}
	return <-finished, forwardErr
}

// invoke runs the graph, publishing into em. The channel is not closed here:
// who closes it is what distinguishes Run from RunLive.
func (r *Runner) invoke(ctx context.Context, em *emitter, in TurnInput) (*TurnResult, error) {
	if in.UserInput == "" {
		err := errs.New(errs.CodeInvalidArgument, "user input must not be empty")
		r.fail(ctx, em, in, runMeta{}, err)
		return nil, err
	}
	if r.hasTools && !r.toolSupport {
		err := errs.New(errs.CodeInvalidArgument,
			"tools are registered but the configured chat provider does not support tool calling")
		r.fail(ctx, em, in, runMeta{}, err)
		return nil, err
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
		return nil, err
	}
	em.send(runCtx, Event{
		Type:         EventEnd,
		RunID:        result.RunID,
		ThreadID:     result.ThreadID,
		FinishReason: string(result.FinishReason),
		Usage:        &result.Usage,
		Warnings:     result.Warnings,
	})
	return result, nil
}

// fail emits the terminal error event. It does not close the event channel:
// closing is the entry point's business, because who closes it — and therefore
// when a consumer stops waiting — is the one thing that differs between Run and
// RunLive. A background context is used for the send so cancellation of the run
// context cannot suppress the error the consumer is draining for.
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
}

// runMeta is the per-run identity minted in Run and read by the graph nodes.
type runMeta struct {
	runID     string
	traceID   string
	threadID  string
	startedAt time.Time
	// spanSeq numbers this run's node spans. It travels in the context rather
	// than living in a map on the runner because its lifetime has to be the
	// run's and nothing else's: the run row is finished inside the finalize
	// node, so anything cleaned up when the run finished would be gone before
	// finalize's own span was written, and that span would be numbered 1 and
	// sort ahead of the ingress it came after.
	spanSeq *int64
}

type runMetaKey struct{}

func withRunMeta(ctx context.Context, meta runMeta) context.Context {
	if meta.spanSeq == nil {
		meta.spanSeq = new(int64)
	}
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

	// Every node is wrapped, not instrumented individually. Six nodes that
	// each opened and closed their own span would drift — one would forget the
	// error branch, another would measure the wrong clock — and the trace is
	// only readable while all six agree on what a span means.
	if err := g.AddLambdaNode(nodeIngress,
		compose.InvokableLambda(traced(r, nodeIngress, r.ingress, ingressDetail))); err != nil {
		return err
	}
	if err := g.AddLambdaNode(nodePlan,
		compose.InvokableLambda(traced(r, nodePlan, r.plan, planDetail))); err != nil {
		return err
	}
	if err := g.AddLambdaNode(nodeTools,
		compose.InvokableLambda(traced(r, nodeTools, r.runTools, toolsDetail))); err != nil {
		return err
	}
	if err := g.AddLambdaNode(nodeClarify,
		compose.InvokableLambda(traced(r, nodeClarify, r.clarifyNode, clarifyDetail))); err != nil {
		return err
	}
	if err := g.AddLambdaNode(nodeAnswer,
		compose.InvokableLambda(traced(r, nodeAnswer, r.answerNode, answerDetail))); err != nil {
		return err
	}
	if err := g.AddLambdaNode(nodeFinalize,
		compose.InvokableLambda(traced(r, nodeFinalize, r.finalize, finalizeDetail))); err != nil {
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
