package httpapi

import (
	"context"
	"errors"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol"

	"github.com/zed1995/platepilot/chat-service/internal/httperr"
	"github.com/zed1995/platepilot/shared/domain/errs"
)

// Validatable is implemented by request DTOs that can validate themselves. It is
// wired into Hertz's binding layer so handlers get one canonical error type for
// both binding and validation failures.
type Validatable interface {
	Validate() error
}

// validatorFunc is installed into the Hertz engine with
// server.WithCustomValidatorFunc. It runs the Validatable contract and maps any
// failure onto the canonical error model.
func validatorFunc(_ *protocol.Request, target any) error {
	validatable, ok := target.(Validatable)
	if !ok {
		return nil
	}
	if err := validatable.Validate(); err != nil {
		return toValidationError(err)
	}
	return nil
}

// BindAndValidate binds an inbound request into target and validates it.
//
// Binding failures (malformed body, wrong types) become invalid_argument;
// validation failures become validation_failed. Handlers should pass the
// returned error straight to httperr.Write.
func BindAndValidate(c *app.RequestContext, target any) error {
	if err := c.BindAndValidate(target); err != nil {
		return normalizeBindingError(err)
	}
	return nil
}

// BindQueryAndValidate binds query parameters into target and validates it.
func BindQueryAndValidate(c *app.RequestContext, target any) error {
	if err := c.BindQuery(target); err != nil {
		return normalizeBindingError(err)
	}
	if err := c.Validate(target); err != nil {
		return normalizeBindingError(err)
	}
	return nil
}

// WriteAndAbort writes the canonical error response and stops the handler chain,
// which is the standard way to reject an invalid request.
func WriteAndAbort(ctx context.Context, c *app.RequestContext, err error) {
	httperr.Write(ctx, c, err)
	c.Abort()
}

func normalizeBindingError(err error) error {
	var already *errs.Error
	if errors.As(err, &already) {
		return err
	}
	return errs.Wrap(errs.CodeInvalidArgument, "request binding failed", err)
}

func toValidationError(err error) error {
	var already *errs.Error
	if errors.As(err, &already) {
		return err
	}
	return errs.Wrap(errs.CodeValidationFailed, err.Error(), err)
}
