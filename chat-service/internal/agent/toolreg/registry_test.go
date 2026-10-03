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

// writingEntry is a tool that changes something outside the conversation.
func writingEntry(name string, policy toolreg.Confirmation) toolreg.Entry {
	entry := goodEntry(name, okHandler())
	entry.Spec.ReadOnly = false
	entry.Confirmation = policy
	if policy == toolreg.ConfirmationRequired {
		entry.SummarizeApproval = func(_ context.Context, raw json.RawMessage) (string, error) {
			return "will run with " + string(raw), nil
		}
	}
	return entry
}

// The confirmation declaration is a startup check, not a runtime one: a writing
// tool whose author never said how it is authorised must not reach the model at
// all, because the failure it guards against is a write nothing approved and
// the first place that can be caught is registration.
func TestRegisterRefusesAWriteWithNoConfirmationPolicy(t *testing.T) {
	cases := []struct {
		name    string
		entry   toolreg.Entry
		wantErr bool
		why     string
	}{
		{
			name:    "write with no policy",
			entry:   writingEntry("no_policy", ""),
			wantErr: true,
			why:     "the omitted declaration is exactly the case the check exists for",
		},
		{
			name:    "write declaring no side effects",
			entry:   writingEntry("claims_none", toolreg.ConfirmationNone),
			wantErr: true,
			why:     "a non-read-only spec cannot claim it has nothing to authorise",
		},
		{
			name:    "write awaiting confirmation",
			entry:   writingEntry("needs_approval", toolreg.ConfirmationRequired),
			wantErr: false,
		},
		{
			name:    "write the user's own words authorise",
			entry:   writingEntry("save_it", toolreg.ConfirmationImplicit),
			wantErr: false,
		},
		{
			name:    "read-only with no policy",
			entry:   goodEntry("plain_read", okHandler()),
			wantErr: false,
			why:     "a read has no side effects to describe",
		},
		{
			name: "read-only declaring none",
			entry: func() toolreg.Entry {
				e := goodEntry("read_none", okHandler())
				e.Confirmation = toolreg.ConfirmationNone
				return e
			}(),
			wantErr: false,
		},
		{
			name: "read-only claiming it needs confirmation",
			entry: func() toolreg.Entry {
				e := goodEntry("read_gated", okHandler())
				e.Confirmation = toolreg.ConfirmationRequired
				return e
			}(),
			wantErr: true,
			why:     "gating a lookup would turn a lookup into a question",
		},
		{
			name: "unknown policy",
			entry: func() toolreg.Entry {
				e := writingEntry("mystery", "maybe")
				return e
			}(),
			wantErr: true,
		},
		{
			name: "gated write with nothing to show the user",
			entry: func() toolreg.Entry {
				e := writingEntry("no_summary", toolreg.ConfirmationRequired)
				e.SummarizeApproval = nil
				return e
			}(),
			wantErr: true,
			why:     "a parked call whose description is missing cannot be approved meaningfully",
		},
		{
			name: "renderer on a tool that never parks",
			entry: func() toolreg.Entry {
				e := writingEntry("stray_summary", toolreg.ConfirmationImplicit)
				e.SummarizeApproval = func(context.Context, json.RawMessage) (string, error) {
					return "never used", nil
				}
				return e
			}(),
			wantErr: true,
			why:     "the renderer exists to describe a park, so having one without a park is a mistake",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := toolreg.New(time.Second).Register(tc.entry)
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("registration succeeded; want an error (%s)", tc.why)
			case !tc.wantErr && err != nil:
				t.Fatalf("registration failed: %v", err)
			}
		})
	}
}

// The gate is asked by name at call time, so the registry has to answer it, and
// it has to answer "no" for a tool that is not there — an unknown tool cannot
// run either, so there is no write to miss.
func TestRegistryReportsWhichToolsNeedConfirmation(t *testing.T) {
	reg := toolreg.New(time.Second)
	if err := reg.Register(writingEntry("request_thing", toolreg.ConfirmationRequired)); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Register(writingEntry("save_thing", toolreg.ConfirmationImplicit)); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Register(goodEntry("read_thing", okHandler())); err != nil {
		t.Fatalf("register: %v", err)
	}

	for name, want := range map[string]bool{
		"request_thing": true,
		"save_thing":    false,
		"read_thing":    false,
		"absent_thing":  false,
	} {
		if got := reg.RequiresConfirmation(name); got != want {
			t.Fatalf("RequiresConfirmation(%q) = %v, want %v", name, got, want)
		}
	}

	// And the helper on the entry agrees with the lookup, so a caller holding
	// the entry does not have to go through the registry.
	if !writingEntry("x", toolreg.ConfirmationRequired).RequiresConfirmation() {
		t.Fatal("an entry declaring ConfirmationRequired must report RequiresConfirmation")
	}
	if writingEntry("x", toolreg.ConfirmationImplicit).RequiresConfirmation() {
		t.Fatal("an entry declaring ConfirmationImplicit must not report RequiresConfirmation")
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
