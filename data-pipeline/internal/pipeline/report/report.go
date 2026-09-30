// Package report accumulates ingestion counters and persists batch audit
// records. It only ever holds counts, line numbers, and reasons: raw review
// text and PII never reach it.
package report

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/review"
	"github.com/zed/platepilot/shared/port"
)

// Collector accumulates one batch's counters and writes the audit records.
// A nil store makes the collector a pure in-memory counter, which is what
// --dry-run uses.
type Collector struct {
	store      port.PipelineStore
	report     review.BatchReport
	rejections []review.Rejection
	missing    map[string]int64
	// reasonCounts holds the M2 rejection counts by quality code. It stays nil
	// until a stage reports one so the jsonb column is not written as an empty
	// object that would read as "checked and found none".
	reasonCounts map[string]int64
	// batchID is the database-assigned identity, known only after Start. Until
	// then it is zero, which is why Reject buffers instead of writing.
	batchID int64
	started bool
}

// New returns a collector for one stage.
func New(store port.PipelineStore, stage, curationVersion, sourceFile string, startedAt time.Time) *Collector {
	return &Collector{
		store: store,
		report: review.BatchReport{
			Stage:           stage,
			CurationVersion: curationVersion,
			SourceFile:      sourceFile,
			StartedAt:       startedAt,
			Status:          review.StatusRunning,
		},
		missing: make(map[string]int64),
	}
}

// BatchID returns the database-assigned batch identifier, or zero before Start.
func (c *Collector) BatchID() int64 { return c.batchID }

// SetSourceSHA256 records the source file hash.
func (c *Collector) SetSourceSHA256(hash string) { c.report.SourceSHA256 = hash }

// SetBoundaryVersion records which administrative boundary release produced the
// borough labels in this batch. It is empty when the run fell back to the
// approximate bounding boxes, so an audit record can never imply exact labels
// it did not compute.
func (c *Collector) SetBoundaryVersion(version string) { c.report.BoundaryVersion = version }

// Start writes the running batch record and adopts the id the database gave it.
//
// The id is assigned by an identity column, so it cannot be known before the
// insert. Rejections buffered before this point are re-stamped in Finish, which
// is the only place they are written.
func (c *Collector) Start(ctx context.Context) error {
	c.started = true
	if c.store == nil {
		return nil
	}
	id, err := c.store.StartBatch(ctx, c.report)
	if err != nil {
		return err
	}
	c.batchID = id
	c.report.BatchID = id
	return nil
}

// RowRead counts one decoded (or undecodable) source line.
func (c *Collector) RowRead() { c.report.RowsRead++ }

// Accepted counts records that passed curation.
func (c *Collector) Accepted(n int64) { c.report.Accepted += n }

// Written counts rows persisted to the database.
func (c *Collector) Written(n int) { c.report.Written += int64(n) }

// Deduped counts duplicate source records collapsed into one document.
func (c *Collector) Deduped(n int64) { c.report.Deduped += n }

// Filtered counts records that are valid but out of scope.
func (c *Collector) Filtered() { c.report.Filtered++ }

// Unmatched counts records that could not be linked to a parent document.
func (c *Collector) Unmatched() { c.report.Unmatched++ }

// MissingField records one absent optional field.
func (c *Collector) MissingField(field string) { c.missing[field]++ }

// Reject records one rejected source line and its reason.
func (c *Collector) Reject(stage string, lineNo int64, reason, sourceRecordID string) {
	c.report.Rejected++
	c.rejections = append(c.rejections, review.Rejection{
		BatchID:        c.batchID,
		Stage:          stage,
		LineNo:         lineNo,
		Reason:         reason,
		SourceRecordID: sourceRecordID,
	})
}

// Rejections returns the buffered rejection records.
//
// It exists for the concurrent embedding path, where each worker buffers into
// its own collector and the run's collector absorbs the records once the
// workers have joined. Returning a copy keeps the caller from mutating the
// buffer, and returning nil when there is nothing keeps the zero value cheap.
func (c *Collector) Rejections() []review.Rejection {
	if len(c.rejections) == 0 {
		return nil
	}
	return append([]review.Rejection(nil), c.rejections...)
}

// Report returns the current snapshot of the batch report.
func (c *Collector) Report() review.BatchReport {
	snapshot := c.report
	snapshot.MissingFields = missingFields(c.missing)
	snapshot.RejectReasons = int64Counts(c.reasonCounts)
	return snapshot
}

// Finish writes the terminal batch record and any rejections.
func (c *Collector) Finish(ctx context.Context, status, errorCode string, now time.Time) error {
	c.report.Status = status
	c.report.ErrorCode = errorCode
	c.report.FinishedAt = now
	c.report.DurationMS = now.Sub(c.report.StartedAt).Milliseconds()
	c.report.MissingFields = missingFields(c.missing)
	c.report.RejectReasons = int64Counts(c.reasonCounts)
	if c.store == nil {
		return nil
	}
	if !c.started {
		return errs.New(errs.CodeInternal, "report: finish called before start")
	}
	if err := c.store.FinishBatch(ctx, c.report); err != nil {
		return err
	}
	if len(c.rejections) > 0 {
		// The batch id is only known after Start, so anything rejected while
		// reading was buffered without it. Stamp it now rather than making
		// every reject path reach through the store.
		for i := range c.rejections {
			c.rejections[i].BatchID = c.batchID
		}
		return c.store.RecordRejections(ctx, c.rejections)
	}
	return nil
}

// int64Counts copies the rejection map, returning nil when there is nothing to
// report. A nil map serialises away under omitempty, which is what keeps an
// import stage's row from carrying an empty quality section.
func int64Counts(counts map[string]int64) map[string]int64 {
	if len(counts) == 0 {
		return nil
	}
	out := make(map[string]int64, len(counts))
	for key, count := range counts {
		out[key] = count
	}
	return out
}

func missingFields(counts map[string]int64) []review.FieldMissing {
	if len(counts) == 0 {
		return nil
	}
	out := make([]review.FieldMissing, 0, len(counts))
	for field, count := range counts {
		out = append(out, review.FieldMissing{Field: field, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

// --- M2 document and embedding counters ------------------------------------

// The methods below add the M2 outcomes without disturbing the M1 semantics.
// The import counters above describe source rows; the M2 ones describe
// generated documents and vectors, which have no source row at all. Folding
// them into RowsRead or Rejected would make an M1 column describe a stage it
// was never written for.

// DocumentsBuilt records documents produced by the document-build stage.
func (c *Collector) DocumentsBuilt(n int) {
	setCount(&c.report.DocumentsBuilt, n)
}

// DocumentsEmbedded records vectors written by the embedding stage.
func (c *Collector) DocumentsEmbedded(n int) {
	setCount(&c.report.DocumentsEmbedded, n)
}

// DocumentsRejected records documents the embedding stage refused to vector.
func (c *Collector) DocumentsRejected(n int) {
	setCount(&c.report.DocumentsRejected, n)
}

// SetEmbeddingModel records which model produced this run's vectors, and
// EmbeddingDimensions how wide they are. Both are empty for stages that do not
// call the provider, so the audit row cannot imply a model was involved.
func (c *Collector) SetEmbedding(model string, dimensions int) {
	c.report.EmbeddingModel = model
	if dimensions > 0 {
		value := dimensions
		c.report.EmbeddingDimensions = &value
	}
}

// RejectReason counts one rejection reason.
//
// The reason is a code, never text: the quality gate produces a small fixed
// set, and an open-ended map is the reason the counts live in a jsonb column
// rather than one column per reason.
func (c *Collector) RejectReason(reason string, n int) {
	if c.reasonCounts == nil {
		c.reasonCounts = make(map[string]int64)
	}
	c.reasonCounts[reason] += int64(n)
}

// RejectDocument records one document the embedding stage refused.
//
// Only the document id and the reason are stored. The vector that failed the
// check is never written to the audit trail: it is 1024 floats, and a table
// of them would be unreadable and enormous. The document id is what makes the
// failure findable after the fact.
func (c *Collector) RejectDocument(stage string, documentID int64, reason string) {
	c.RejectReason(reason, 1)
	c.rejections = append(c.rejections, review.Rejection{
		BatchID:        c.batchID,
		Stage:          stage,
		LineNo:         documentID,
		Reason:         reason,
		SourceRecordID: strconv.FormatInt(documentID, 10),
	})
}

// setCount adds n to a nullable counter, leaving it null until something is
// actually counted. A stage that never reports a count must not be readable as
// a run that counted zero.
func setCount(target **int64, n int) {
	if n == 0 {
		return
	}
	if *target == nil {
		value := int64(n)
		*target = &value
		return
	}
	**target += int64(n)
}
