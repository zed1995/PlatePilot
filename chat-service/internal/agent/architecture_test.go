package agent_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestEinoIsConfinedToTheAgentBoundary enforces the M4 architecture rule:
//
//  1. No package outside chat-service/internal/agent may name any Eino type —
//     the transports, the retrieval read path, and shared/ must program against
//     project-owned ports.
//  2. Eino's message/schema DTOs (schema.Message and friends) are narrower
//     still: only the einomodel adapter and the tool registry bridge may touch
//     them. The graph runtime in package agent may use compose and the model
//     interface, but every node passes project DTOs.
//
// It walks the whole repository so a future service (data-pipeline included)
// cannot quietly start depending on the agent framework.
func TestEinoIsConfinedToTheAgentBoundary(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}

	agentRoot := filepath.Join("chat-service", "internal", "agent")
	schemaAllowedDirs := []string{
		filepath.Join(agentRoot, "einomodel") + string(os.PathSeparator),
		filepath.Join(agentRoot, "toolreg") + string(os.PathSeparator),
	}

	var checked int
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == "vendor" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		imports, parseErr := parseEinoTestImports(path)
		if parseErr != nil {
			return parseErr
		}
		checked++

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		for _, imp := range imports {
			if imp != "github.com/cloudwego/eino" &&
				!strings.HasPrefix(imp, "github.com/cloudwego/eino/") {
				continue
			}
			if !strings.HasPrefix(rel, filepath.ToSlash(agentRoot)+"/") {
				t.Errorf("%s imports %q; Eino may only be used under %s",
					rel, imp, agentRoot)
				continue
			}
			if imp == "github.com/cloudwego/eino/schema" ||
				strings.HasPrefix(imp, "github.com/cloudwego/eino/schema/") {
				if !inAnyDir(rel, schemaAllowedDirs) {
					t.Errorf("%s imports %q; Eino schema DTOs may only be used in einomodel/ and toolreg/",
						rel, imp)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
	if checked == 0 {
		t.Fatal("no Go files were inspected")
	}
}

func inAnyDir(slashPath string, dirs []string) bool {
	for _, dir := range dirs {
		if strings.HasPrefix(slashPath, filepath.ToSlash(dir)) {
			return true
		}
	}
	return false
}

func parseEinoTestImports(path string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	imports := make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		value, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		imports = append(imports, value)
	}
	return imports, nil
}
