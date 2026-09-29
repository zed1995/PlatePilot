// Package report accumulates ingestion counters and persists batch audit
// records. It only ever holds counts, line numbers, and reasons: raw review
// text and PII never reach it.
package report

import (
	"context"
	"sort"
	"time"

	"github.com/zed/platepilot/shared/domain/review"
	"github.com/zed/platepilot/shared/idgen"
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
}

// New returns a collector for one stage.
func New(store port.PipelineStore, stage, curationVersion, sourceFile string, startedAt time.Time) *Collector {
	return &Collector{
		store: store,
		report: review.BatchReport{
			BatchID:         idgen.NewUUID(),
			Stage:           stage,
			CurationVersion: curationVersion,
			SourceFile:      sourceFile,
			StartedAt:       startedAt,
			Status:          review.StatusRunning,
		},
		missing: make(map[string]int64),
	}
}

// BatchID returns the generated batch identifier.
func (c *Collector) BatchID() string { return c.report.BatchID }

// SetSourceSHA256 records the source file hash.
func (c *Collector) SetSourceSHA256(hash string) { c.report.SourceSHA256 = hash }

// Start writes the running batch record.
func (c *Collector) Start(ctx context.Context) error {
	if c.store == nil {
		return nil
	}
	return c.store.StartBatch(ctx, c.report)
}

// RowRead counts one decoded (or undecodable) source line.
func (c *Collector) RowRead() { c.report.RowsRead++ }

// Accepted counts records that passed curation.
func (c *Collector) Accepted(n int64) { c.report.Accepted += n }

// Written counts documents persisted to Atlas.
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
		BatchID:        c.report.BatchID,
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
	if err := c.store.FinishBatch(ctx, c.report); err != nil {
		return err
	}
	if len(c.rejections) > 0 {
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
