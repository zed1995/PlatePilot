package app

import (
	"context"
	"errors"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// fakeChatModel is a queue-driven model for the assembly tests.
//
// It lives in the app package rather than shared/testkit because testkit is
// shared code and deliberately knows nothing about Eino: the framework types
// stop at the assembly boundary, and a fake that implemented them would drag
// the dependency back into the shared module.
type fakeChatModel struct {
	mu sync.Mutex

	name string
	// resps is the completion queue; chunks is the streamed script. An empty
	// stream queue serves the next completion as one chunk, so a test that only
	// queued a completion does not have to care whether streaming is on.
	resps  []*schema.Message
	chunks []*schema.Message
	err    error
	// gate, when set, holds every call until the test releases it.
	gate func(context.Context) error

	requests [][]*schema.Message
	calls    int
}

var _ model.ToolCallingChatModel = (*fakeChatModel)(nil)

// WithTools returns the same instance: the tests do not distinguish bound from
// unbound calls, they distinguish which model was called at all.
func (m *fakeChatModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *fakeChatModel) Generate(
	ctx context.Context, in []*schema.Message, _ ...model.Option,
) (*schema.Message, error) {
	if err := m.wait(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, in)
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	if len(m.resps) == 0 {
		return nil, errors.New("fake chat model: no response queued")
	}
	resp := m.resps[0]
	m.resps = m.resps[1:]
	return resp, nil
}

func (m *fakeChatModel) Stream(
	ctx context.Context, in []*schema.Message, _ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if err := m.wait(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.requests = append(m.requests, in)
	m.calls++
	chunks := m.chunks
	if len(chunks) == 0 && len(m.resps) > 0 {
		chunks = []*schema.Message{m.resps[0]}
		m.resps = m.resps[1:]
	}
	m.mu.Unlock()

	reader, writer := schema.Pipe[*schema.Message](0)
	go func() {
		defer writer.Close()
		for _, chunk := range chunks {
			if writer.Send(chunk, nil) {
				return
			}
		}
	}()
	return reader, nil
}

func (m *fakeChatModel) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.gate != nil {
		return m.gate(ctx)
	}
	return nil
}
