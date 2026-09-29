package mongo

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/mongo"

	"github.com/zed/platepilot/shared/domain/errs"
)

// operationError converts a driver error into the shared error model. Mongo and
// BSON types never escape this package, so every returned error is a domain
// error carrying a stable, machine-readable code.
func operationError(op string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		return errs.Wrap(errs.CodeNotFound, op+": document not found", err)
	case mongo.IsDuplicateKeyError(err):
		return errs.Wrap(errs.CodeConflict, op+": duplicate key", err)
	case errors.Is(err, context.DeadlineExceeded), mongo.IsTimeout(err):
		return errs.Wrap(errs.CodeProviderTimeout, op+": mongodb operation timed out", err)
	default:
		return errs.Wrap(errs.CodeProviderUnavailable, op+": mongodb operation failed", err)
	}
}

// isNamespaceExists reports whether a CreateCollection call failed because the
// collection already exists (server error code 48).
func isNamespaceExists(err error) bool {
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		return cmdErr.Code == 48 // NamespaceExists
	}
	return false
}

// isIndexConflict reports whether index creation failed because an index with
// the same name already exists with a different definition.
func isIndexConflict(err error) bool {
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		switch cmdErr.Code {
		case 85, 86: // IndexOptionsConflict, IndexKeySpecsConflict
			return true
		}
	}
	return false
}
