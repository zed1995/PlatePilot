package pipeline

import (
	"context"
	"time"

	"github.com/zed1995/platepilot/data-pipeline/internal/pipeline/curate"
	"github.com/zed1995/platepilot/data-pipeline/internal/pipeline/report"
	"github.com/zed1995/platepilot/shared/domain/review"
	"github.com/zed1995/platepilot/shared/store"
)

// newStageCollector opens an audit record for one M2 stage.
//
// A dry-run gets a nil store, so the collector counts in memory and writes
// nothing. That is the same rule the import stages follow: a dry-run that left
// a "succeeded" batch row behind would be indistinguishable from a real run in
// the audit table.
func newStageCollector(stores Stores, dryRun bool, stage string) *report.Collector {
	var store store.PipelineStore
	if !dryRun {
		store = stores.Pipeline
	}
	return report.New(store, stage, curate.CurationVersion, "", time.Now().UTC())
}

// finishStage writes the terminal batch record.
//
// The counters are reported on the failure path too. A run that died halfway is
// the one case where the partial count is the whole point of the record, and a
// report that only ever appears on success cannot answer "how far did it get".
//
// The finish is best-effort: a stage whose real work succeeded must not be
// reported as failed because the audit write failed. That is why the error from
// Finish is dropped — the run's own outcome is the one the caller acts on, and
// a failed audit write should not mask a successful build.
func finishStage(
	collector *report.Collector,
	runErr error,
	count func(*report.Collector),
) {
	if collector == nil {
		return
	}
	count(collector)
	status, code := review.StatusSucceeded, ""
	if runErr != nil {
		status, code = review.StatusFailed, codeOf(runErr)
	}
	// The run's own context may already be cancelled by whatever failed it, and
	// the audit row is the only record of how far it got, so the write is given
	// a context that is not.
	_ = collector.Finish(context.WithoutCancel(context.Background()), status, code, time.Now().UTC())
}
