package toolreg_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"

	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
)

func goodEntry(name string, handler toolreg.Handler) toolreg.Entry {
	return toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name:        name,
			Description: "test tool",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"word": {"type": "string"},
					"count": {"type": "integer"}
				},
				"required": ["word"],
				"additionalProperties": false
			}`),
			ReadOnly:  true,
			TimeoutMS: 50,
		},
		Handler: handler,
	}
}

func okHandler() toolreg.Handler {
	return func(_ context.Context, raw json.RawMessage) (domaintool.ToolResult, error) {
		return domaintool.ToolResult{Content: "got " + string(raw)}, nil
	}
}

func TestRegisterValidation(t *testing.T) {
	cases := []struct {
		name    string
		entry   toolreg.Entry
		wantErr bool
	}{
		{name: "valid", entry: goodEntry("good_tool", okHandler()), wantErr: false},
		{name: "nil handler", entry: toolreg.Entry{Spec: domaintool.ToolSpec{Name: "no_handler"}}, wantErr: true},
		{name: "bad name uppercase", entry: goodEntry("BadName", okHandler()), wantErr: true},
		{name: "bad name start digit", entry: goodEntry("1tool", okHandler()), wantErr: true},
		{name: "bad name too short", entry: goodEntry("a", okHandler()), wantErr: true},
		{name: "bad name dash", entry: goodEntry("bad-name", okHandler()), wantErr: true},
		{name: "malformed schema json", entry: func() toolreg.Entry {
			e := goodEntry("bad_schema", okHandler())
			e.Spec.Parameters = json.RawMessage(`{not json`)
			return e
		}(), wantErr: true},
		{name: "schema root not object", entry: func() toolreg.Entry {
			e := goodEntry("array_root", okHandler())
			e.Spec.Parameters = json.RawMessage(`{"type":"array"}`)
			return e
		}(), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := toolreg.New(time.Second)
			err := reg.Register(tc.entry)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	t.Run("duplicate name", func(t *testing.T) {
		reg := toolreg.New(time.Second)
		if err := reg.Register(goodEntry("dup_tool", okHandler())); err != nil {
			t.Fatalf("first register: %v", err)
		}
		if err := reg.Register(goodEntry("dup_tool", okHandler())); err == nil {
			t.Fatal("expected duplicate error")
		}
	})

	t.Run("specs carry timeout and order", func(t *testing.T) {
		reg := toolreg.New(700 * time.Millisecond)
		if err := reg.Register(goodEntry("zeta_tool", okHandler())); err != nil {
			t.Fatal(err)
		}
		if err := reg.Register(goodEntry("alpha_tool", okHandler())); err != nil {
			t.Fatal(err)
		}
		specs := reg.Specs()
		if len(specs) != 2 || specs[0].Name != "alpha_tool" || specs[1].Name != "zeta_tool" {
			t.Fatalf("specs = %+v", specs)
		}
		if specs[1].TimeoutMS != 50 {
			t.Fatalf("TimeoutMS = %d, want spec value 50", specs[1].TimeoutMS)
		}
	})

	t.Run("default timeout applies", func(t *testing.T) {
		reg := toolreg.New(300 * time.Millisecond)
		entry := goodEntry("default_timeout_tool", okHandler())
		entry.Spec.TimeoutMS = 0
		if err := reg.Register(entry); err != nil {
			t.Fatal(err)
		}
		got, ok := reg.TimeoutOf("default_timeout_tool")
		if !ok || got != 300*time.Millisecond {
			t.Fatalf("timeout = %s ok=%v", got, ok)
		}
	})
}

func TestInvoke(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		reg := toolreg.New(time.Second)
		if err := reg.Register(goodEntry("ok_tool", okHandler())); err != nil {
			t.Fatal(err)
		}
		result := reg.Invoke(context.Background(), domaintool.ToolCall{
			ID: "call-1", Name: "ok_tool", Arguments: json.RawMessage(`{"word":"hi"}`),
		})
		if result.Status != domaintool.ToolStatusOK {
			t.Fatalf("status = %s err = %v", result.Status, result.Error)
		}
		if result.CallID != "call-1" || result.Name != "ok_tool" {
			t.Fatalf("result identity = %+v", result)
		}
	})

	t.Run("unknown tool", func(t *testing.T) {
		reg := toolreg.New(time.Second)
		result := reg.Invoke(context.Background(), domaintool.ToolCall{Name: "ghost"})
		if result.Status != domaintool.ToolStatusError || result.Error == nil ||
			result.Error.Code != errs.CodeNotFound {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		reg := toolreg.New(time.Second)
		if err := reg.Register(goodEntry("bad_json_tool", okHandler())); err != nil {
			t.Fatal(err)
		}
		result := reg.Invoke(context.Background(), domaintool.ToolCall{
			Name: "bad_json_tool", Arguments: json.RawMessage(`{oops`),
		})
		if result.Error == nil || result.Error.Code != errs.CodeValidationFailed {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("missing required", func(t *testing.T) {
		reg := toolreg.New(time.Second)
		if err := reg.Register(goodEntry("missing_required_tool", okHandler())); err != nil {
			t.Fatal(err)
		}
		result := reg.Invoke(context.Background(), domaintool.ToolCall{
			Name: "missing_required_tool", Arguments: json.RawMessage(`{"count":3}`),
		})
		if result.Error == nil || result.Error.Code != errs.CodeValidationFailed {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("wrong type", func(t *testing.T) {
		reg := toolreg.New(time.Second)
		if err := reg.Register(goodEntry("wrong_type_tool", okHandler())); err != nil {
			t.Fatal(err)
		}
		result := reg.Invoke(context.Background(), domaintool.ToolCall{
			Name: "wrong_type_tool", Arguments: json.RawMessage(`{"word":123}`),
		})
		if result.Error == nil || result.Error.Code != errs.CodeValidationFailed {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("additional property rejected", func(t *testing.T) {
		reg := toolreg.New(time.Second)
		if err := reg.Register(goodEntry("strict_tool", okHandler())); err != nil {
			t.Fatal(err)
		}
		result := reg.Invoke(context.Background(), domaintool.ToolCall{
			Name: "strict_tool", Arguments: json.RawMessage(`{"word":"hi","nope":1}`),
		})
		if result.Error == nil || result.Error.Code != errs.CodeValidationFailed {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		reg := toolreg.New(time.Second)
		entry := goodEntry("slow_tool", func(ctx context.Context, _ json.RawMessage) (domaintool.ToolResult, error) {
			<-ctx.Done()
			return domaintool.ToolResult{}, ctx.Err()
		})
		entry.Timeout = 20 * time.Millisecond
		if err := reg.Register(entry); err != nil {
			t.Fatal(err)
		}
		result := reg.Invoke(context.Background(), domaintool.ToolCall{
			Name: "slow_tool", Arguments: json.RawMessage(`{"word":"go"}`),
		})
		if result.Error == nil || result.Error.Code != errs.CodeProviderTimeout {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("panic recovered", func(t *testing.T) {
		reg := toolreg.New(time.Second)
		entry := goodEntry("panic_tool", func(_ context.Context, _ json.RawMessage) (domaintool.ToolResult, error) {
			panic("boom")
		})
		if err := reg.Register(entry); err != nil {
			t.Fatal(err)
		}
		result := reg.Invoke(context.Background(), domaintool.ToolCall{
			Name: "panic_tool", Arguments: json.RawMessage(`{"word":"x"}`),
		})
		if result.Error == nil || result.Error.Code != errs.CodeInternal {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("handler domain error preserved", func(t *testing.T) {
		reg := toolreg.New(time.Second)
		entry := goodEntry("domain_err_tool", func(_ context.Context, _ json.RawMessage) (domaintool.ToolResult, error) {
			return domaintool.ToolResult{}, errs.New(errs.CodeConflict, "nope")
		})
		if err := reg.Register(entry); err != nil {
			t.Fatal(err)
		}
		result := reg.Invoke(context.Background(), domaintool.ToolCall{
			Name: "domain_err_tool", Arguments: json.RawMessage(`{"word":"x"}`),
		})
		if result.Error == nil || result.Error.Code != errs.CodeConflict {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("handler generic error becomes internal", func(t *testing.T) {
		reg := toolreg.New(time.Second)
		entry := goodEntry("generic_err_tool", func(_ context.Context, _ json.RawMessage) (domaintool.ToolResult, error) {
			return domaintool.ToolResult{}, errors.New("kaboom")
		})
		if err := reg.Register(entry); err != nil {
			t.Fatal(err)
		}
		result := reg.Invoke(context.Background(), domaintool.ToolCall{
			Name: "generic_err_tool", Arguments: json.RawMessage(`{"word":"x"}`),
		})
		if result.Error == nil || result.Error.Code != errs.CodeInternal {
			t.Fatalf("result = %+v", result)
		}
	})
}
