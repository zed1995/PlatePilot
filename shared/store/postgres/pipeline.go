package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/review"
)

// PipelineStore is the PostgreSQL implementation of store.PipelineStore.
type PipelineStore struct {
	client *Client
}

// NewPipelineStore returns a Postgres-backed audit store.
func NewPipelineStore(client *Client) *PipelineStore {
	return &PipelineStore{client: client}
}

// batchInsertSQL writes a batch report on first observation.
//
// A batch row is written twice per run: once when the run starts (status
// running) and once when it finishes. The insert is therefore idempotent on the
// primary key and the finish is a plain UPDATE, which keeps a crashed run
// visible as "running" rather than losing it.
const batchInsertSQL = `
INSERT INTO ingestion_batches (
	stage, curation_version, source_file, source_sha256, boundary_version,
	started_at, duration_ms, rows_read, accepted, written, deduped, filtered,
	rejected, unmatched, missing_fields, status, error_code,
	documents_built, documents_embedded, documents_rejected,
	embedding_model, embedding_dimensions, reject_reasons
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17,
	$18, $19, $20, $21, $22, $23)
ON CONFLICT DO NOTHING
RETURNING id`

const batchUpdateSQL = `
UPDATE ingestion_batches SET
	finished_at   = $2,
	duration_ms   = $3,
	rows_read     = $4,
	accepted      = $5,
	written       = $6,
	deduped       = $7,
	filtered      = $8,
	rejected      = $9,
	unmatched     = $10,
	missing_fields= $11,
	status        = $12,
	error_code    = $13,
	source_sha256 = COALESCE($14, source_sha256),
	boundary_version = COALESCE($15, boundary_version),
	documents_built = COALESCE($16, documents_built),
	documents_embedded = COALESCE($17, documents_embedded),
	documents_rejected = COALESCE($18, documents_rejected),
	embedding_model = COALESCE($19, embedding_model),
	embedding_dimensions = COALESCE($20, embedding_dimensions),
	reject_reasons = COALESCE($21, reject_reasons)
WHERE id = $1`

// batchArgs maps a report onto the ingestion_batches row. The boundary version
// is carried on the report itself rather than passed in, so the audit record
// always states which geometry release produced the borough labels.
// batchJSON holds the two jsonb columns a report may carry, already encoded.
// They are encoded once here because both the insert and the update need them
// and a report is written twice per run.
type batchJSON struct {
	missing    []byte
	rejections any
}

func batchArgs(r review.BatchReport) ([]any, batchJSON, error) {
	missing, err := marshalOrNil(r.MissingFields)
	if err != nil {
		return nil, batchJSON{}, err
	}
	// Reject reasons stay NULL rather than an empty object when the stage did
	// not run a quality gate, so the column answers "was anything checked".
	reasons, err := marshalOrNilOrNil(r.RejectReasons)
	if err != nil {
		return nil, batchJSON{}, err
	}
	// r.BatchID is not sent: the identity column assigns it.
	return []any{
		r.Stage, r.CurationVersion,
		nullString(r.SourceFile), nullString(r.SourceSHA256), nullString(r.BoundaryVersion),
		r.StartedAt, r.DurationMS, r.RowsRead, r.Accepted, r.Written, r.Deduped,
		r.Filtered, r.Rejected, r.Unmatched, missing, r.Status, nullString(r.ErrorCode),
		r.DocumentsBuilt, r.DocumentsEmbedded, r.DocumentsRejected,
		nullString(r.EmbeddingModel), r.EmbeddingDimensions, reasons,
	}, batchJSON{missing: missing, rejections: reasons}, nil
}

// StartBatch records that an import stage has begun and returns its new id.
func (s *PipelineStore) StartBatch(ctx context.Context, r review.BatchReport) (int64, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	args, _, err := batchArgs(r)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := s.client.pool.QueryRow(ctx, batchInsertSQL, args...).Scan(&id); err != nil {
		return 0, operationError("postgres: start batch", err)
	}
	return id, nil
}

// FinishBatch records the outcome of an import stage.
func (s *PipelineStore) FinishBatch(ctx context.Context, r review.BatchReport) error {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	_, encoded, err := batchArgs(r)
	if err != nil {
		return err
	}
	// Every argument after the first is positional, and the statement uses
	// COALESCE for the optional ones so a report that does not carry an M2
	// value leaves the running row's value alone rather than clearing it.
	tag, err := s.client.pool.Exec(ctx, batchUpdateSQL,
		r.BatchID, r.FinishedAt, r.DurationMS, r.RowsRead, r.Accepted, r.Written,
		r.Deduped, r.Filtered, r.Rejected, r.Unmatched, encoded.missing, r.Status,
		nullString(r.ErrorCode), nullString(r.SourceSHA256), nullString(r.BoundaryVersion),
		r.DocumentsBuilt, r.DocumentsEmbedded, r.DocumentsRejected,
		nullString(r.EmbeddingModel), r.EmbeddingDimensions, encoded.rejections)
	if err != nil {
		return operationError("postgres: finish batch", err)
	}
	if tag.RowsAffected() == 0 {
		// The run was never started, or the batch belongs to another database.
		return errs.Newf(errs.CodeNotFound, "postgres: no batch %d to finish", r.BatchID)
	}
	return nil
}

// RecordRejections appends audit records for rejected source lines.
//
// Only the line number, reason, and source id are stored: never review text or
// any other field that could carry PII.
func (s *PipelineStore) RecordRejections(ctx context.Context, items []review.Rejection) error {
	if len(items) == 0 {
		return nil
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	batch := pgx.Batch{}
	for _, item := range items {
		batch.Queue(`
			INSERT INTO ingestion_rejections (batch_id, stage, line_no, reason, source_record_id)
			VALUES ($1, $2, $3, $4, $5)`,
			item.BatchID, item.Stage, item.LineNo, item.Reason, nullString(item.SourceRecordID))
	}
	results := s.client.pool.SendBatch(ctx, &batch)
	defer results.Close()
	for range items {
		if _, err := results.Exec(); err != nil {
			return operationError("postgres: record rejections", err)
		}
	}
	return nil
}

// ListBatches returns the most recent batch reports, newest first.
func (s *PipelineStore) ListBatches(ctx context.Context, limit int) ([]review.BatchReport, error) {
	query := "SELECT " + batchColumns + " FROM ingestion_batches"
	args := []any{}
	if limit > 0 {
		query += " ORDER BY started_at DESC LIMIT $1"
		args = append(args, limit)
	} else {
		query += " ORDER BY started_at DESC"
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	rows, err := s.client.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, operationError("postgres: list batches", err)
	}
	defer rows.Close()
	out := make([]review.BatchReport, 0)
	for rows.Next() {
		var row batchRow
		if err := s.scanBatch(&row, rows); err != nil {
			return nil, err
		}
		item, err := row.toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate batches", err)
	}
	return out, nil
}

// scanBatch reads one batch row.
func (s *PipelineStore) scanBatch(row *batchRow, rows pgx.Rows) error {
	if err := rows.Scan(
		&row.ID, &row.Stage, &row.CurationVersion, &row.SourceFile, &row.SourceSHA256,
		&row.BoundaryVersion, &row.StartedAt, &row.FinishedAt, &row.DurationMS,
		&row.RowsRead, &row.Accepted, &row.Written, &row.Deduped, &row.Filtered,
		&row.Rejected, &row.Unmatched, &row.MissingFields, &row.Status, &row.ErrorCode,
		&row.DocumentsBuilt, &row.DocumentsEmbedded, &row.DocumentsRejected,
		&row.EmbeddingModel, &row.EmbeddingDimensions, &row.RejectReasons,
	); err != nil {
		return operationError("postgres: scan batch", err)
	}
	return nil
}

// BatchDetail returns one batch report and its rejections.
func (s *PipelineStore) BatchDetail(ctx context.Context, batchID int64) (review.BatchReport, []review.Rejection, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	row := s.client.pool.QueryRow(ctx,
		"SELECT "+batchColumns+" FROM ingestion_batches WHERE id = $1", batchID)
	var batch batchRow
	err := row.Scan(
		&batch.ID, &batch.Stage, &batch.CurationVersion, &batch.SourceFile, &batch.SourceSHA256,
		&batch.BoundaryVersion, &batch.StartedAt, &batch.FinishedAt, &batch.DurationMS,
		&batch.RowsRead, &batch.Accepted, &batch.Written, &batch.Deduped, &batch.Filtered,
		&batch.Rejected, &batch.Unmatched, &batch.MissingFields, &batch.Status, &batch.ErrorCode,
		&batch.DocumentsBuilt, &batch.DocumentsEmbedded, &batch.DocumentsRejected,
		&batch.EmbeddingModel, &batch.EmbeddingDimensions, &batch.RejectReasons,
	)
	if err != nil {
		if pgErrNoRows(err) {
			return review.BatchReport{}, nil, errs.Newf(errs.CodeNotFound, "postgres: no batch %d", batchID)
		}
		return review.BatchReport{}, nil, operationError("postgres: get batch", err)
	}
	report, err := batch.toDomain()
	if err != nil {
		return review.BatchReport{}, nil, err
	}
	rejections, err := s.listRejections(ctx, batchID)
	if err != nil {
		return review.BatchReport{}, nil, err
	}
	return report, rejections, nil
}

// listRejections returns the audit records for one batch, in line order.
func (s *PipelineStore) listRejections(ctx context.Context, batchID int64) ([]review.Rejection, error) {
	rows, err := s.client.pool.Query(ctx, `
		SELECT batch_id, stage, line_no, reason, source_record_id
		FROM ingestion_rejections WHERE batch_id = $1 ORDER BY line_no`, batchID)
	if err != nil {
		return nil, operationError("postgres: list rejections", err)
	}
	defer rows.Close()
	out := make([]review.Rejection, 0)
	for rows.Next() {
		var item review.Rejection
		var sourceID *string
		if err := rows.Scan(&item.BatchID, &item.Stage, &item.LineNo, &item.Reason, &sourceID); err != nil {
			return nil, operationError("postgres: scan rejection", err)
		}
		if sourceID != nil {
			item.SourceRecordID = *sourceID
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate rejections", err)
	}
	return out, nil
}
