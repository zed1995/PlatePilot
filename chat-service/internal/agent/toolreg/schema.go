package toolreg

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// schema is the small, deliberately closed subset of JSON Schema that tool
// parameters in this project may use. It is validated locally instead of with a
// third-party validator because the contract is narrow: the registry needs to
// reject malformed model arguments at the edge, and a bespoke checker keeps
// "why was this rejected" one function away.
//
// Supported keywords: type ("object"|"array"|"string"|"number"|"integer"|
// "boolean"|"null"), required, properties, items, enum, and
// additionalProperties:false. Anything else is accepted and ignored rather than
// rejected, so descriptions and future annotations never break registration.
type schemaNode struct {
	Type                 string
	Required             []string
	Properties           map[string]*schemaNode
	Items                *schemaNode
	Enum                 []any
	AdditionalProperties *bool
}

// parseSchema decodes one JSON Schema object. Boolean schemas and non-objects
// are rejected: tool parameters are always JSON objects in this project.
func parseSchema(raw json.RawMessage) (*schemaNode, error) {
	if len(raw) == 0 {
		// Absent schema means "no parameters".
		return &schemaNode{Type: "object"}, nil
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("parameters is not valid JSON: %w", err)
	}
	node, err := buildNode(generic)
	if err != nil {
		return nil, err
	}
	if node.Type != "" && node.Type != "object" {
		return nil, fmt.Errorf("parameters schema root must declare type \"object\" (got %q)", node.Type)
	}
	node.Type = "object"
	return node, nil
}

func buildNode(generic map[string]any) (*schemaNode, error) {
	node := &schemaNode{}
	if raw, ok := generic["type"]; ok {
		switch v := raw.(type) {
		case string:
			node.Type = v
		default:
			return nil, fmt.Errorf("schema \"type\" must be a string, got %T", raw)
		}
	}
	if raw, ok := generic["required"]; ok {
		items, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("schema \"required\" must be an array")
		}
		for _, item := range items {
			name, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("schema \"required\" entries must be strings")
			}
			node.Required = append(node.Required, name)
		}
	}
	if raw, ok := generic["properties"]; ok {
		props, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema \"properties\" must be an object")
		}
		node.Properties = make(map[string]*schemaNode, len(props))
		keys := make([]string, 0, len(props))
		for name := range props {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			child, ok := props[name].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("property %q schema must be an object", name)
			}
			built, err := buildNode(child)
			if err != nil {
				return nil, fmt.Errorf("property %q: %w", name, err)
			}
			node.Properties[name] = built
		}
	}
	if raw, ok := generic["items"]; ok {
		child, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema \"items\" must be a schema object")
		}
		built, err := buildNode(child)
		if err != nil {
			return nil, fmt.Errorf("items: %w", err)
		}
		node.Items = built
	}
	if raw, ok := generic["enum"]; ok {
		items, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("schema \"enum\" must be an array")
		}
		node.Enum = items
	}
	if raw, ok := generic["additionalProperties"]; ok {
		allowed, ok := raw.(bool)
		if !ok {
			// Schema-form additionalProperties is outside the supported subset;
			// leave it unset rather than rejecting a perfectly descriptive schema.
			node.AdditionalProperties = nil
		} else {
			node.AdditionalProperties = &allowed
		}
	}
	return node, nil
}

// validate checks already-decoded arguments against the node. The path is used
// in error messages so the model can tell which argument was wrong.
func (n *schemaNode) validate(value any, path string) error {
	if err := n.validateType(value, path); err != nil {
		return err
	}
	if len(n.Enum) > 0 && !enumContains(n.Enum, value) {
		return fmt.Errorf("%s must be one of %s", path, joinEnums(n.Enum))
	}
	switch typed := value.(type) {
	case map[string]any:
		if err := n.validateObject(typed, path); err != nil {
			return err
		}
	case []any:
		if n.Items != nil {
			for i, item := range typed {
				if err := n.Items.validate(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (n *schemaNode) validateObject(obj map[string]any, path string) error {
	for _, name := range n.Required {
		if v, present := obj[name]; !present || v == nil {
			return fmt.Errorf("%s is required", joinPath(path, name))
		}
	}
	for name, value := range obj {
		child, known := n.Properties[name]
		if !known {
			if n.AdditionalProperties != nil && !*n.AdditionalProperties {
				return fmt.Errorf("%s is not an allowed parameter", joinPath(path, name))
			}
			continue
		}
		if value == nil {
			continue // null on an optional property is tolerated at the edge
		}
		if err := child.validate(value, joinPath(path, name)); err != nil {
			return err
		}
	}
	return nil
}

func (n *schemaNode) validateType(value any, path string) error {
	if n.Type == "" {
		return nil
	}
	ok := false
	switch n.Type {
	case "object":
		_, ok = value.(map[string]any)
	case "array":
		_, ok = value.([]any)
	case "string":
		_, ok = value.(string)
	case "boolean":
		_, ok = value.(bool)
	case "number":
		_, ok = value.(float64)
	case "integer":
		f, isNumber := value.(float64)
		ok = isNumber && f == math.Trunc(f) && !math.IsInf(f, 0)
	case "null":
		ok = value == nil
	default:
		return fmt.Errorf("schema declares unsupported type %q", n.Type)
	}
	if !ok {
		return fmt.Errorf("%s must be %s (got %s)", path, n.Type, jsonType(value))
	}
	return nil
}

func enumContains(enum []any, value any) bool {
	for _, candidate := range enum {
		if jsonEqual(candidate, value) {
			return true
		}
	}
	return false
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func joinEnums(enum []any) string {
	parts := make([]string, len(enum))
	for i, v := range enum {
		raw, _ := json.Marshal(v)
		parts[i] = string(raw)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func joinPath(base, name string) string {
	if base == "" || base == "$" {
		return name
	}
	return base + "." + name
}

func jsonType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", value)
	}
}
