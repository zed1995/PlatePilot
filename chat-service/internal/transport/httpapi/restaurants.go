package httpapi

import (
	"context"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/zed/platepilot/chat-service/internal/transport/httperr"
	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/retrieval"
	"github.com/zed/platepilot/shared/domain/search"
)

// SearchService is the read path this transport talks to.
//
// It is an interface rather than a concrete service so the handler is testable
// without a repository, a database, or a model. The application layer supplies
// the implementation; nothing here knows how candidates are found.
type SearchService interface {
	Search(ctx context.Context, req retrieval.Request) (retrieval.SearchResult, error)
}

// EvidenceService is the read path's evidence recall.
//
// It is a separate interface from SearchService rather than a second method on
// it, because the two have different preconditions: a search may be asked with
// only a filter, while an evidence recall without named restaurants is refused.
// Splitting them lets each handler state its own rule and lets a test exercise
// either without the other.
type EvidenceService interface {
	Evidence(ctx context.Context, req EvidenceQuery) (EvidenceResult, error)
}

// EvidenceResult is what the evidence service returns to this layer.
type EvidenceResult struct {
	Evidence []evidence.Evidence
	Trace    *EvidenceTrace
}

// EvidenceQuery is the transport-level shape of an evidence request.
type EvidenceQuery struct {
	// RestaurantIDs is required. The transport repeats the domain's rule rather
	// than relying on the service alone, because the failure it prevents is a
	// successful response carrying someone else's reviews.
	RestaurantIDs []int64            `json:"restaurant_ids"`
	Query         string             `json:"query,omitempty"`
	Topic         string             `json:"topic,omitempty"`
	DocTypes      []evidence.DocType `json:"doc_types,omitempty"`
	TopK          int                `json:"top_k,omitempty"`
	// TokenBudget bounds what assembly keeps. Zero means the service default.
	TokenBudget int `json:"token_budget,omitempty"`
}

// Validate is deliberately not implemented on this type.
//
// The scoping rule is checked after the route has merged the path's restaurant
// id into the request. A Validate method would run inside BindAndValidate,
// before that merge, and would reject the single-restaurant route's own request
// shape — which is the request with no body. Validating at the one point where
// the complete request exists keeps one rule in one place.
func (q EvidenceQuery) Validate() error { return nil }

// requireScope rejects a request that names no restaurant.
func (q EvidenceQuery) requireScope() error {
	for _, id := range q.RestaurantIDs {
		if id > 0 {
			return nil
		}
	}
	return errs.New(errs.CodeRetrievalNoScope,
		"an evidence request needs at least one restaurant_id: a citation is a "+
			"claim about a specific restaurant")
}

// EvidenceBundle is the assembled answer the transport returns.
//
// It carries the evidence and the trace together, for the same reason the
// search response does: a citation a reader cannot check is an assertion, and a
// trace a reader cannot see is a log line.
type EvidenceBundle struct {
	Evidence []evidence.Evidence `json:"evidence"`
	Trace    *EvidenceTrace      `json:"trace,omitempty"`
}

// EvidenceTrace is the transport-level projection of the recall trace.
type EvidenceTrace struct {
	Query       string `json:"query,omitempty"`
	ScopeSize   int    `json:"scope_size"`
	Topic       string `json:"topic,omitempty"`
	Recalled    int    `json:"recalled"`
	Kept        int    `json:"kept"`
	Dropped     int    `json:"dropped"`
	Tokens      int    `json:"tokens"`
	TokenBudget int    `json:"token_budget"`
	// DroppedByReason explains the budget, so a short answer reads as a budget
	// decision rather than as a thin corpus.
	DroppedByReason   map[string]int `json:"dropped_by_reason,omitempty"`
	EmbeddingModelID  string         `json:"embedding_model_id,omitempty"`
	QueryEmbeddingDim int            `json:"query_embedding_dim,omitempty"`
	Warnings          []string       `json:"warnings,omitempty"`
}

// SearchRequest is the inbound body of a restaurant search.
//
// Text and Query are both accepted because callers mean different things by
// them: Text is a name or address fragment, Query is a whole question.
type SearchRequest struct {
	Query  string                  `json:"query,omitempty"`
	Text   string                  `json:"text,omitempty"`
	Filter search.RestaurantFilter `json:"filter,omitempty"`
	TopK   int                     `json:"top_k,omitempty"`
}

// SearchResponse is what a search returns. The trace travels beside the
// candidates on purpose: a candidate without its derivation is an assertion the
// user cannot check, and the trace is what lets a reviewer see which condition
// surfaced each result.
type SearchResponse struct {
	Candidates []search.RestaurantCandidate `json:"candidates"`
	Trace      *retrieval.Trace             `json:"trace,omitempty"`
}

// SearchHandler answers POST /v1/restaurants/search.
func SearchHandler(service SearchService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		var body SearchRequest
		if err := BindAndValidate(c, &body); err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		result, err := service.Search(ctx, body.toDomain())
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		if result.Candidates == nil {
			// An empty result is a successful search that found nothing, not an
			// error. Reporting it as one would train callers to retry a question
			// that has no answer.
			result.Candidates = []search.RestaurantCandidate{}
		}
		c.JSON(200, result)
	}
}

// Validate rejects a request that carries nothing to search with.
//
// Deeper filter rules live in the domain, where both repository implementations
// enforce them; duplicating them here would only give the two copies a chance to
// disagree.
func (r SearchRequest) Validate() error {
	if r.Query == "" && r.Text == "" && r.Filter.IsEmpty() {
		return errs.New(errs.CodeRetrievalEmptyQuery,
			"a search needs query, text, or a filter")
	}
	return r.Filter.Validate()
}

// toDomain converts the wire body into the domain request.
func (r SearchRequest) toDomain() retrieval.Request {
	return retrieval.Request{
		Query:  r.Query,
		Text:   r.Text,
		Filter: r.Filter,
		TopK:   r.TopK,
	}
}

// RestaurantEvidenceHandler answers POST /v1/restaurants/{id}/evidence.
//
// The restaurant id is in the path and is prepended to whatever the body names.
// It is not read from the body alone: a route that says /restaurants/7/evidence
// and then takes the restaurant from the payload would let a caller ask about
// one restaurant and be answered about another, which is the exact confusion
// this layer's scoping exists to prevent.
func RestaurantEvidenceHandler(service EvidenceService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		restaurantID, err := pathID(c, "id")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		body, err := bindEvidence(c)
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		// The path wins on conflict. The body's own id is dropped rather than
		// rejected: a client that sends both has made the route unambiguous, and
		// failing over a redundant field teaches the caller nothing.
		body.RestaurantIDs = append([]int64{restaurantID},
			withoutID(body.RestaurantIDs, restaurantID)...)
		writeEvidence(ctx, c, service, body)
	}
}

// EvidenceHandler answers POST /v1/restaurants/evidence.
func EvidenceHandler(service EvidenceService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		body, err := bindEvidence(c)
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		writeEvidence(ctx, c, service, body)
	}
}

// writeEvidence validates, recalls, and renders one evidence request.
func writeEvidence(
	ctx context.Context, c *app.RequestContext, service EvidenceService, body EvidenceQuery,
) {
	if err := body.requireScope(); err != nil {
		WriteAndAbort(ctx, c, err)
		return
	}
	result, err := service.Evidence(ctx, body)
	if err != nil {
		httperr.Write(ctx, c, err)
		return
	}
	// An empty set is an empty array, never null and never an error: "this
	// restaurant has nothing on record" is a real answer, and reporting it as a
	// failure would teach callers to retry a question that has no answer.
	if result.Evidence == nil {
		result.Evidence = []evidence.Evidence{}
	}
	c.JSON(200, EvidenceBundle{Evidence: result.Evidence, Trace: result.Trace})
}

// bindEvidence reads the request body.
//
// An absent body is an empty query rather than a parse failure: the
// single-restaurant route supplies the id from the path, so requiring a body
// there would make the simplest request the one clients most often get wrong.
func bindEvidence(c *app.RequestContext) (EvidenceQuery, error) {
	var body EvidenceQuery
	if err := BindAndValidate(c, &body); err != nil {
		return EvidenceQuery{}, err
	}
	return body, nil
}

// pathID reads a positive integer path parameter.
func pathID(c *app.RequestContext, name string) (int64, error) {
	raw := c.Param(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errs.Newf(errs.CodeInvalidArgument,
			"%s must be a positive integer, got %q", name, raw)
	}
	return id, nil
}

// withoutID removes every copy of one id from a list.
func withoutID(ids []int64, id int64) []int64 {
	out := make([]int64, 0, len(ids))
	for _, candidate := range ids {
		if candidate != id {
			out = append(out, candidate)
		}
	}
	return out
}
