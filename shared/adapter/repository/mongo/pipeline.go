package mongo

import (
	"context"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/review"
)

// PipelineStore is the Mongo implementation of port.PipelineStore.
type PipelineStore struct {
	client *Client
}

// NewPipelineStore returns a Mongo-backed ingestion audit store.
func NewPipelineStore(client *Client) *PipelineStore {
	return &PipelineStore{client: client}
}

func (s *PipelineStore) batches() *mongo.Collection {
	return s.client.collection(CollectionIngestionBatches)
}

func (s *PipelineStore) rejections() *mongo.Collection {
	return s.client.collection(CollectionIngestionRejections)
}

// StartBatch records a running batch report.
func (s *PipelineStore) StartBatch(ctx context.Context, report review.BatchReport) error {
	if strings.TrimSpace(report.BatchID) == "" {
		return errs.New(errs.CodeInvalidArgument, "batch_id is required")
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	_, err := s.batches().ReplaceOne(ctx, bson.M{"_id": report.BatchID}, batchToDoc(report), options.Replace().SetUpsert(true))
	return operationError("mongo: start batch", err)
}

// FinishBatch writes the final batch report.
func (s *PipelineStore) FinishBatch(ctx context.Context, report review.BatchReport) error {
	if strings.TrimSpace(report.BatchID) == "" {
		return errs.New(errs.CodeInvalidArgument, "batch_id is required")
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	_, err := s.batches().ReplaceOne(ctx, bson.M{"_id": report.BatchID}, batchToDoc(report), options.Replace().SetUpsert(true))
	return operationError("mongo: finish batch", err)
}

// RecordRejections appends rejection records for a batch.
func (s *PipelineStore) RecordRejections(ctx context.Context, items []review.Rejection) error {
	if len(items) == 0 {
		return nil
	}
	models := make([]mongo.WriteModel, 0, len(items))
	for _, item := range items {
		models = append(models, mongo.NewReplaceOneModel().
			SetFilter(bson.M{"_id": rejectionID(item)}).
			SetReplacement(rejectionToDoc(item)).
			SetUpsert(true))
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	_, err := s.rejections().BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	return operationError("mongo: record rejections", err)
}

// ListBatches returns recent batches, newest first.
func (s *PipelineStore) ListBatches(ctx context.Context, limit int) ([]review.BatchReport, error) {
	findOpts := options.Find().SetSort(bson.D{{Key: "started_at", Value: -1}, {Key: "_id", Value: -1}})
	if limit > 0 {
		findOpts.SetLimit(int64(limit))
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	cursor, err := s.batches().Find(ctx, bson.M{}, findOpts)
	if err != nil {
		return nil, operationError("mongo: list batches", err)
	}
	defer cursor.Close(context.Background())

	out := make([]review.BatchReport, 0)
	for cursor.Next(ctx) {
		var doc batchDoc
		if err := cursor.Decode(&doc); err != nil {
			return nil, operationError("mongo: decode batch", err)
		}
		out = append(out, docToBatch(doc))
	}
	if err := cursor.Err(); err != nil {
		return nil, operationError("mongo: iterate batches", err)
	}
	return out, nil
}

// BatchDetail returns one batch and its rejections.
func (s *PipelineStore) BatchDetail(ctx context.Context, batchID string) (review.BatchReport, []review.Rejection, error) {
	if strings.TrimSpace(batchID) == "" {
		return review.BatchReport{}, nil, errs.New(errs.CodeInvalidArgument, "batch_id is required")
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	var doc batchDoc
	if err := s.batches().FindOne(ctx, bson.M{"_id": batchID}).Decode(&doc); err != nil {
		return review.BatchReport{}, nil, operationError("mongo: get batch", err)
	}

	cursor, err := s.rejections().Find(ctx, bson.M{"batch_id": batchID}, options.Find().SetSort(bson.D{{Key: "line_no", Value: 1}}))
	if err != nil {
		return review.BatchReport{}, nil, operationError("mongo: list rejections", err)
	}
	defer cursor.Close(context.Background())

	out := make([]review.Rejection, 0)
	for cursor.Next(ctx) {
		var rej rejectionDoc
		if err := cursor.Decode(&rej); err != nil {
			return review.BatchReport{}, nil, operationError("mongo: decode rejection", err)
		}
		out = append(out, review.Rejection{
			BatchID:        rej.BatchID,
			Stage:          rej.Stage,
			LineNo:         rej.LineNo,
			Reason:         rej.Reason,
			SourceRecordID: rej.SourceRecordID,
		})
	}
	if err := cursor.Err(); err != nil {
		return review.BatchReport{}, nil, operationError("mongo: iterate rejections", err)
	}
	return docToBatch(doc), out, nil
}
