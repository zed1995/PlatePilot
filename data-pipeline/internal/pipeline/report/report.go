// Package report accumulates ingestion counters and persists batch audit
// records. It only ever holds counts, line numbers, and reasons: raw review
// text and PII never reach it.
package report

import (
	"context"
	"sort"
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

// Report returns the current snapshot of the batch report.
func (c *Collector) Report() review.BatchReport {
	snapshot := c.report
	snapshot.MissingFields = missingFields(c.missing)
	return snapshot
}

// Finish writes the terminal batch record and any rejections.
func (c *Collector) Finish(ctx context.Context, status, errorCode string, now time.Time) error {
	c.report.Status = status
	c.report.ErrorCode = errorCode
	c.report.FinishedAt = now
	c.report.DurationMS = now.Sub(c.report.StartedAt).Milliseconds()
	c.report.MissingFields = missingFields(c.missing)
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
