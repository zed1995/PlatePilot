package toolreg_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	einoschema "github.com/cloudwego/eino/schema"
	domaintool "github.com/zed/platepilot/shared/domain/tool"

	"github.com/zed/platepilot/chat-service/internal/agent/toolreg"
)

func TestEinoToolsRoundTrip(t *testing.T) {
	reg := toolreg.New(time.Second)
	entry := goodEntry("round_trip_tool", func(_ context.Context, raw json.RawMessage) (domaintool.ToolResult, error) {
		return domaintool.ToolResult{Content: "echo " + string(raw)}, nil
	})
	if err := reg.Register(entry); err != nil {
		t.Fatal(err)
	}

	infos, err := reg.ToolInfos()
	if err != nil {
		t.Fatalf("ToolInfos: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("infos len = %d", len(infos))
	}
	if infos[0].Name != "round_trip_tool" {
		t.Fatalf("info name = %q", infos[0].Name)
	}
	jsonSchema, err := infos[0].ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("ToJSONSchema: %v", err)
	}
	if jsonSchema.Type != "object" {
		t.Fatalf("schema type = %q, want object", jsonSchema.Type)
	}
	prop, ok := jsonSchema.Properties.Get("word")
	if !ok || prop == nil || prop.Type != "string" {
		t.Fatalf("word property missing or mistyped: %+v", jsonSchema.Properties)
	}

	tools, err := reg.EinoTools()
	if err != nil {
		t.Fatalf("EinoTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools len = %d", len(tools))
	}
	info, err := tools[0].Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "round_trip_tool" {
		t.Fatalf("Info name = %q", info.Name)
	}

	out, err := tools[0].InvokableRun(context.Background(), `{"word":"hello"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var envelope domaintool.ToolResult
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("result is not a ToolResult envelope: %v\nraw: %s", err, out)
	}
	if envelope.Status != domaintool.ToolStatusOK || envelope.Content == "" {
		t.Fatalf("envelope = %+v", envelope)
	}
}

// TestEinoToolSatisfiesInvokableTool is a compile-time assertion that the
// adapter returned by the registry is an Eino InvokableTool.
func TestEinoToolSatisfiesInvokableTool(t *testing.T) {
	reg := toolreg.New(time.Second)
	if err := reg.Register(goodEntry("iface_tool", okHandler())); err != nil {
		t.Fatal(err)
	}
	tools, err := reg.EinoTools()
	if err != nil {
		t.Fatal(err)
	}
	var _ einotool.InvokableTool = tools[0]
	_ = einoschema.ToolInfo{}
}
