package toolreg

import (
	"context"
	"encoding/json"
	"fmt"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	einojsonschema "github.com/eino-contrib/jsonschema"

	domaintool "github.com/zed/platepilot/shared/domain/tool"
)

// einoTool bridges one registered entry onto Eino's tool.InvokableTool so the
// same registry can drive a native Eino ToolsNode. The graph itself invokes
// Registry.Invoke directly (it needs call IDs for tool messages); this adapter
// exists for the Eino-native execution path and is tested independently.
type einoTool struct {
	reg  *Registry
	spec domaintool.ToolSpec
	info *schema.ToolInfo
}

// EinoTools builds one Eino InvokableTool per registered entry, in name order.
func (r *Registry) EinoTools() ([]einotool.InvokableTool, error) {
	specs := r.Specs()
	tools := make([]einotool.InvokableTool, 0, len(specs))
	for _, spec := range specs {
		info, err := toolInfo(spec)
		if err != nil {
			return nil, err
		}
		tools = append(tools, &einoTool{reg: r, spec: spec, info: info})
	}
	return tools, nil
}

// ToolInfos returns the Eino tool descriptions for binding onto the chat model.
func (r *Registry) ToolInfos() ([]*schema.ToolInfo, error) {
	specs := r.Specs()
	infos := make([]*schema.ToolInfo, 0, len(specs))
	for _, spec := range specs {
		info, err := toolInfo(spec)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func toolInfo(spec domaintool.ToolSpec) (*schema.ToolInfo, error) {
	info := &schema.ToolInfo{
		Name: spec.Name,
		Desc: spec.Description,
	}
	if len(spec.Parameters) > 0 {
		jsonSchema := &einojsonschema.Schema{}
		if err := json.Unmarshal(spec.Parameters, jsonSchema); err != nil {
			return nil, fmt.Errorf("toolreg: parse schema of tool %q for Eino: %w", spec.Name, err)
		}
		info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(jsonSchema)
	}
	return info, nil
}

// Info implements einotool.BaseTool.
func (t *einoTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

// InvokableRun implements einotool.InvokableTool. The ToolResult envelope is
// serialized verbatim, so an error status (unknown tool, invalid arguments,
// timeout) is delivered back to the model as tool content rather than aborting
// the graph.
func (t *einoTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...einotool.Option) (string, error) {
	result := t.reg.Invoke(ctx, domaintool.ToolCall{
		Name:      t.spec.Name,
		Arguments: json.RawMessage(argumentsInJSON),
	})
	raw, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("toolreg: serialize result of tool %q: %w", t.spec.Name, err)
	}
	return string(raw), nil
}
