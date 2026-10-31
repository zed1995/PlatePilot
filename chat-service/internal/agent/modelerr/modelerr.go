// Package modelerr maps chat-model failures onto PlatePilot's error codes.
//
// The nodes call a model, not a provider, so the transport's own error
// vocabulary never reaches them. Mapping in one place keeps the HTTP status the
// user sees a property of the failure's cause — a timeout and a refused
// connection are both "the model did not answer", but only one of them is worth
// retrying — rather than of which node happened to be running.
package modelerr

import (
	"context"
	"errors"
	"net/url"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"

	"github.com/zed1995/platepilot/shared/domain/errs"
)

// Map classifies err.
//
// Errors that already carry a PlatePilot code are returned untouched: a node
// that produced a citation violation or an invalid argument is not a provider
// failure, and re-coding it here would tell the client to retry a request that
// cannot succeed as written. Cancellation is likewise not a failure of the
// model: the caller went away.
func Map(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errs.As(err); ok {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errs.Wrap(errs.CodeProviderTimeout, "模型调用超时", err)
	}

	var apiErr *einoopenai.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.HTTPStatusCode == 408 || apiErr.HTTPStatusCode == 504:
			return errs.Wrap(errs.CodeProviderTimeout, "模型服务响应超时", err)
		case apiErr.HTTPStatusCode == 429 || apiErr.HTTPStatusCode >= 500:
			return errs.Wrap(errs.CodeProviderUnavailable, "模型服务暂时不可用", err)
		}
	}

	// A *url.Error is what an unreachable host, a refused connection, or a
	// transport-level timeout surfaces as. Its Timeout report is the only way
	// to tell the retryable case from the terminal one.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() || errors.Is(urlErr.Err, context.DeadlineExceeded) {
			return errs.Wrap(errs.CodeProviderTimeout, "模型服务响应超时", err)
		}
		return errs.Wrap(errs.CodeProviderUnavailable, "模型服务连接失败", err)
	}

	return errs.Wrap(errs.CodeProviderUnavailable, "模型调用失败", err)
}
