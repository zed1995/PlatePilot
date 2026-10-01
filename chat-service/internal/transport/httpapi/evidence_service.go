package httpapi

import (
	"context"

	"github.com/zed/platepilot/chat-service/internal/retrieval"
	domainretrieval "github.com/zed/platepilot/shared/domain/retrieval"
)

// evidenceAdapter bridges the transport's request type onto the retrieval
// service.
//
// The two shapes are kept separate rather than sharing one struct so that a
// field added to the service's request does not silently become part of the HTTP
// contract. A wire format is a promise; a request struct is not.
type evidenceAdapter struct {
	service *retrieval.Service
}

// NewEvidenceService exposes the retrieval service through the transport port.
func NewEvidenceService(service *retrieval.Service) EvidenceService {
	if service == nil {
		return nil
	}
	return evidenceAdapter{service: service}
}

func (a evidenceAdapter) Evidence(
	ctx context.Context, req EvidenceQuery,
) (EvidenceResult, error) {
	items, trace, err := a.service.AssembleAndReturn(ctx, retrieval.EvidenceRequest{
		RestaurantIDs: req.RestaurantIDs,
		Query:         req.Query,
		Topic:         req.Topic,
		DocTypes:      req.DocTypes,
		TopK:          req.TopK,
	}, retrieval.AssembleOptions{TokenBudget: req.TokenBudget})
	if err != nil {
		return EvidenceResult{}, err
	}
	return EvidenceResult{Evidence: items, Trace: projectTrace(trace)}, nil
}

// projectTrace maps the domain trace onto the wire shape.
//
// The mapping is explicit field by field so that adding a field to the domain
// trace cannot change the response body by accident. An API that grows a field
// on every trace change is an API nobody can version.
func projectTrace(trace *domainretrieval.EvidenceTrace) *EvidenceTrace {
	if trace == nil {
		return nil
	}
	return &EvidenceTrace{
		Query:             trace.Query,
		ScopeSize:         trace.ScopeSize,
		Topic:             trace.Topic,
		Recalled:          trace.Recalled,
		Kept:              trace.Kept,
		Dropped:           trace.Dropped,
		Tokens:            trace.Tokens,
		TokenBudget:       trace.TokenBudget,
		DroppedByReason:   trace.DroppedByReason,
		EmbeddingModelID:  trace.EmbeddingModelID,
		QueryEmbeddingDim: trace.QueryEmbeddingDim,
		Warnings:          trace.Warnings,
	}
}
