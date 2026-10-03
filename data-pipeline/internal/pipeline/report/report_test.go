package report

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/review"
)

// fakeStore records what a collector wrote, so the tests can assert on the
// terminal report without a database.
type fakeStore struct {
	started    []review.BatchReport
	finished   []review.BatchReport
	rejections []review.Rejection
	nextID     int64
}

func (s *fakeStore) StartBatch(_ context.Context, r review.BatchReport) (int64, error) {
	if r.Stage == "" {
		return 0, errs.New(errs.CodeInvalidArgument, "stage is required")
	}
	s.nextID++
	r.BatchID = s.nextID
	s.started = append(s.started, r)
	return r.BatchID, nil
}

func (s *fakeStore) FinishBatch(_ context.Context, r review.BatchReport) error {
	s.finished = append(s.finished, r)
	return nil
}

func (s *fakeStore) RecordRejections(_ context.Context, items []review.Rejection) error {
	s.rejections = append(s.rejections, items...)
	return nil
}

func (s *fakeStore) ListBatches(context.Context, int) ([]review.BatchReport, error) {
	return nil, nil
}

func (s *fakeStore) BatchDetail(context.Context, int64) (review.BatchReport, []review.Rejection, error) {
	return review.BatchReport{}, nil, errors.New("not used")
}

func start(t *testing.T, store *fakeStore) *Collector {
	t.Helper()
	collector := New(store, review.StageEmbedding, "rules:v1", "", time.Now().UTC())
	if err := collector.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return collector
}

// A stage that reports nothing must leave the M2 columns absent. The nil
// pointers are what separate "this stage did not apply" from "this stage
// counted zero", and a plain zero would make an import batch claim it produced
// no documents.
func TestM2CountersStayAbsentWhenUnreported(t *testing.T) {
	store := &fakeStore{}
	collector := start(t, store)
	collector.RowRead()
	collector.Written(3)
	if err := collector.Finish(context.Background(), review.StatusSucceeded, "", time.Now().UTC()); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	got := store.finished[0]
	if got.DocumentsBuilt != nil || got.DocumentsEmbedded != nil || got.DocumentsRejected != nil {
		t.Errorf("M2 counters reported by an M1-shaped stage: %+v", got)
	}
	if got.EmbeddingModel != "" || got.EmbeddingDimensions != nil {
		t.Errorf("embedding identity reported by a stage that used no model: %+v", got)
	}
	if got.RejectReasons != nil {
		t.Errorf("reject_reasons = %+v, want nil", got.RejectReasons)
	}
}

// Counting zero must still leave the counter absent. A run that embedded
// nothing is meaningfully different from a run that never got as far as
// embedding, and a report that cannot tell them apart is not an audit record.
func TestZeroDoesNotMaterialiseACounter(t *testing.T) {
	store := &fakeStore{}
	collector := start(t, store)
	collector.DocumentsEmbedded(0)
	collector.SetEmbedding("test-model", 0)
	if err := collector.Finish(context.Background(), review.StatusSucceeded, "", time.Now().UTC()); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	got := store.finished[0]
	if got.DocumentsEmbedded != nil {
		t.Errorf("documents_embedded = %v, want nil for a zero count", got.DocumentsEmbedded)
	}
	if got.EmbeddingDimensions != nil {
		t.Errorf("embedding_dimensions = %v, want nil for an unset width", got.EmbeddingDimensions)
	}
	if got.EmbeddingModel != "test-model" {
		t.Errorf("embedding_model = %q, want the model to be recorded", got.EmbeddingModel)
	}
}

func TestCountersAccumulate(t *testing.T) {
	store := &fakeStore{}
	collector := start(t, store)
	collector.DocumentsBuilt(10)
	collector.DocumentsBuilt(5)
	collector.DocumentsEmbedded(12)
	collector.DocumentsRejected(3)
	collector.SetEmbedding("qwen3-embedding:0.6b", 1024)
	collector.RejectReason("embedding_zero_vector", 2)
	collector.RejectReason("embedding_zero_vector", 1)
	collector.RejectReason("embedding_duplicate", 4)
	if err := collector.Finish(context.Background(), review.StatusSucceeded, "", time.Now().UTC()); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	got := store.finished[0]
	if got.DocumentsBuilt == nil || *got.DocumentsBuilt != 15 {
		t.Errorf("documents_built = %v, want 15", got.DocumentsBuilt)
	}
	if got.DocumentsEmbedded == nil || *got.DocumentsEmbedded != 12 {
		t.Errorf("documents_embedded = %v, want 12", got.DocumentsEmbedded)
	}
	if got.DocumentsRejected == nil || *got.DocumentsRejected != 3 {
		t.Errorf("documents_rejected = %v, want 3", got.DocumentsRejected)
	}
	if got.EmbeddingDimensions == nil || *got.EmbeddingDimensions != 1024 {
		t.Errorf("embedding_dimensions = %v, want 1024", got.EmbeddingDimensions)
	}
	if got.RejectReasons["embedding_zero_vector"] != 3 || got.RejectReasons["embedding_duplicate"] != 4 {
		t.Errorf("reject_reasons = %+v, want zero_vector=3 duplicate=4", got.RejectReasons)
	}
}

// A rejected document is recorded by id and reason only. The vector that
// failed the check is never written: 1024 floats per rejection would make the
// audit table unreadable and enormous.
func TestRejectDocumentRecordsIdAndReasonOnly(t *testing.T) {
	store := &fakeStore{}
	collector := start(t, store)
	collector.RejectDocument(review.StageEmbedding, 4242, "embedding_zero_vector")
	if err := collector.Finish(context.Background(), review.StatusSucceeded, "", time.Now().UTC()); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if len(store.rejections) != 1 {
		t.Fatalf("rejections = %d, want 1", len(store.rejections))
	}
	got := store.rejections[0]
	if got.Reason != "embedding_zero_vector" {
		t.Errorf("reason = %q", got.Reason)
	}
	if got.LineNo != 4242 || got.SourceRecordID != "4242" {
		t.Errorf("document id not recorded: %+v", got)
	}
	// The batch id is only known after Start, so it is stamped at Finish.
	if got.BatchID != store.finished[0].BatchID {
		t.Errorf("batch id = %d, want %d", got.BatchID, store.finished[0].BatchID)
	}
	if store.finished[0].RejectReasons["embedding_zero_vector"] != 1 {
		t.Errorf("reject_reasons = %+v, want one zero_vector", store.finished[0].RejectReasons)
	}
}

// The snapshot a caller reads mid-run must already carry the reasons, so a
// progress line printed during a run does not under-report.
func TestReportSnapshotIncludesReasons(t *testing.T) {
	collector := New(nil, review.StageEmbedding, "rules:v1", "", time.Now().UTC())
	collector.RejectReason("embedding_nan", 1)
	if got := collector.Report().RejectReasons["embedding_nan"]; got != 1 {
		t.Errorf("snapshot reject_reasons = %+v, want nan=1", collector.Report().RejectReasons)
	}
}

// A nil store is the dry-run path: the collector still counts, and writes
// nothing.
func TestNilStoreCountsWithoutWriting(t *testing.T) {
	collector := New(nil, review.StageDocuments, "rules:v1", "", time.Now().UTC())
	if err := collector.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	collector.DocumentsBuilt(4)
	if err := collector.Finish(context.Background(), review.StatusSucceeded, "", time.Now().UTC()); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if got := collector.Report().DocumentsBuilt; got == nil || *got != 4 {
		t.Errorf("documents_built = %v, want 4", got)
	}
}
