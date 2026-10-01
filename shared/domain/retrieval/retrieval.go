// Package retrieval defines the vendor-neutral DTOs of the two-level recall:
// the requests that enter it, the scores that explain its output, and the trace
// that records what it did.
//
// It depends only on the standard library and the other domain packages, so a
// ranking can be reasoned about, tested, and replayed without a running service.
package retrieval

import (
	"github.com/zed/platepilot/shared/domain/search"
)

// Channel names one recall strategy. The set is closed: a new strategy needs a
// new constant here, because every score in a trace is attributed to one.
type Channel string

const (
	// ChannelStructured is the deterministic hard-filter channel. It is the only
	// channel whose results are guaranteed to satisfy every stated condition.
	ChannelStructured Channel = "structured"
	// ChannelKeyword matches names and addresses literally.
	ChannelKeyword Channel = "keyword"
	// ChannelVector matches restaurant descriptions semantically.
	ChannelVector Channel = "vector"
)

// AllChannels lists every channel in a stable order, so reports and tests that
// iterate channels produce the same output every run.
var AllChannels = []Channel{ChannelStructured, ChannelKeyword, ChannelVector}

// ChannelHit is one candidate's raw contribution from one channel.
//
// Score is the channel's own scale, not a fused one: a keyword score is a
// similarity in [0,1] while a structured hit carries no score at all. Fusion
// normalizes per channel; nothing downstream may compare two channels' raw
// scores.
type ChannelHit struct {
	RestaurantID int64
	Score        float64
	// Reason is the human-readable explanation of why this candidate was
	// recalled. It names what matched, not merely that something did: a reason
	// a reviewer cannot check is a reason nobody will.
	Reason string
	// Detail carries the fields the reason was derived from, for the trace.
	Detail map[string]any
}

// ChannelScore is one channel's contribution to one fused candidate.
type ChannelScore struct {
	Channel Channel `json:"channel"`
	Raw     float64 `json:"raw_score"`
	Weight  float64 `json:"weight"`
	// Normalized is the channel score after per-channel min-max scaling, in
	// [0,1]. It is kept beside Raw so a ranking can be explained in terms of
	// what the channel actually produced.
	Normalized float64 `json:"normalized_score"`
	Contrib    float64 `json:"contribution"`
	Reason     string  `json:"reason,omitempty"`
}

// CandidateScore is one fused candidate with its full derivation.
type CandidateScore struct {
	RestaurantID int64          `json:"restaurant_id"`
	Total        float64        `json:"total"`
	Channels     []ChannelScore `json:"channels,omitempty"`
}

// ChannelSummary is what one channel did in aggregate, including the case where
// it did nothing. A trace that omits a silent channel cannot distinguish "this
// channel found nothing" from "this channel was never run".
type ChannelSummary struct {
	Channel Channel `json:"channel"`
	Ran     bool    `json:"ran"`
	// Weight and Results are recorded even when the channel was skipped, so a
	// degraded search reads as a weighted result rather than a mystery.
	Weight  float64 `json:"weight"`
	Results int     `json:"results"`
	// Note explains a skipped or degraded channel in one sentence.
	Note string `json:"note,omitempty"`
}

// Trace records how a candidate list was produced.
//
// Every fused candidate carries the channels that surfaced it, so a ranking can
// always be taken apart. This is what makes the result explainable rather than
// merely correct.
type Trace struct {
	Query string `json:"query,omitempty"`
	// Channels is ordered by AllChannels for stable output.
	Channels []ChannelSummary `json:"channels,omitempty"`
	// Candidates is the per-candidate derivation, aligned with the returned
	// candidates by index.
	Candidates []CandidateScore `json:"candidates,omitempty"`
	// CandidatePool is how many distinct restaurants entered fusion, which is
	// larger than the returned count whenever a channel was cut short.
	CandidatePool int                     `json:"candidate_pool"`
	Returned      int                     `json:"returned"`
	TopK          int                     `json:"top_k"`
	Filters       search.RestaurantFilter `json:"filters,omitempty"`
	// EmbeddingModelID and QueryEmbeddingDim are recorded when the vector
	// channel ran, so a retrieval can be tied to the model that produced it.
	EmbeddingModelID  string `json:"embedding_model_id,omitempty"`
	QueryEmbeddingDim int    `json:"query_embedding_dim,omitempty"`
	// RerankApplied and RerankModelID distinguish "never configured" (empty
	// model) from "configured but failed" (model set, not applied).
	RerankApplied bool   `json:"rerank_applied"`
	RerankModelID string `json:"rerank_model_id,omitempty"`
	// Warnings carries degradations that did not stop the search.
	Warnings []string `json:"warnings,omitempty"`
}

// Warn appends a degradation note, keeping the first occurrence only so a
// repeated failure does not fill the trace with copies of itself.
func (t *Trace) Warn(message string) {
	for _, existing := range t.Warnings {
		if existing == message {
			return
		}
	}
	t.Warnings = append(t.Warnings, message)
}

// Request is the input to one restaurant search.
//
// The channels a request runs are decided by the service configuration and by
// what the request actually contains, not by the caller naming them: a caller
// asks a question, and the service decides which strategies can answer it.
type Request struct {
	Query string `json:"query,omitempty"`
	// Text is free text that may match a name, an address, or both. It is kept
	// separate from Query because the semantic channel consumes Query (a whole
	// question) while the keyword channel consumes Text (a name fragment).
	Text   string                  `json:"text,omitempty"`
	Filter search.RestaurantFilter `json:"filter,omitempty"`
	TopK   int                     `json:"top_k,omitempty"`
}

// HasQuery reports whether the request carries any free text at all.
func (r Request) HasQuery() bool { return r.Text != "" || r.Query != "" }

// SearchResult is what one restaurant search returns.
//
// The candidates and their trace travel together because neither is useful
// alone: a candidate without its derivation is an assertion the user cannot
// check, and a trace without its candidates is a log line.
type SearchResult struct {
	Candidates []search.RestaurantCandidate `json:"candidates"`
	Trace      *Trace                       `json:"trace,omitempty"`
}

// EvidenceTrace records how one evidence set was produced.
//
// It exists separately from Trace because the two answer different questions. A
// candidate trace explains an ordering; an evidence trace explains a set of
// quotes, and what a reviewer needs to check about a quote is different from what
// they need to check about a rank: that it came from the restaurant being talked
// about, that its source resolves, and that the same paragraph was not counted
// twice.
type EvidenceTrace struct {
	Query string `json:"query,omitempty"`
	// ScopeSize is how many restaurants the recall was allowed to read. It is
	// recorded because it is the boundary that makes a citation checkable: every
	// returned document must belong to one of these.
	ScopeSize int    `json:"scope_size"`
	Topic     string `json:"topic,omitempty"`
	TopK      int    `json:"top_k"`
	// Recalled is how many documents the store returned, before any assembly or
	// deduplication. It is kept beside the final count so a reader can tell a
	// small answer from a heavily filtered one.
	Recalled int `json:"recalled"`
	// EmbeddingModelID and QueryEmbeddingDim are recorded when the recall was
	// ranked by similarity. Both are empty for the unordered read, which is a
	// real mode rather than a degraded one.
	EmbeddingModelID  string `json:"embedding_model_id,omitempty"`
	QueryEmbeddingDim int    `json:"query_embedding_dim,omitempty"`
	// Assembly accounting. These are filled in by the assembly stage and stay
	// zero when a caller only recalls, so a trace that reports them means
	// assembly ran and its decisions are visible.
	Kept            int            `json:"kept"`
	Dropped         int            `json:"dropped"`
	Tokens          int            `json:"tokens"`
	TokenBudget     int            `json:"token_budget"`
	DroppedByReason map[string]int `json:"dropped_by_reason,omitempty"`
	// Warnings carries degradations that did not stop the recall.
	Warnings []string `json:"warnings,omitempty"`
}

// Warn appends a degradation note, keeping the first occurrence only.
func (t *EvidenceTrace) Warn(message string) {
	for _, existing := range t.Warnings {
		if existing == message {
			return
		}
	}
	t.Warnings = append(t.Warnings, message)
}
