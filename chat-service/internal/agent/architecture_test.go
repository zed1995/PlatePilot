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
// No package outside chat-service/internal/agent may name any Eino type —
// the transports, the retrieval read path, and shared/ must program against
// project-owned ports. Inside the agent boundary the graph runtime speaks
// Eino's own model interfaces and schema.Message directly; there is no
// adapter layer between the nodes and the framework.
//
// The one exception is chat-service/internal/app: it is the assembly package
// and has to construct the Eino models from configuration and name
// model.BaseChatModel on its Deps. That is wiring, not business logic, and
// keeping it there is why no other package needs the framework.
//
// It walks the whole repository so a future service (data-pipeline included)
// cannot quietly start depending on the agent framework.
func TestEinoIsConfinedToTheAgentBoundary(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}

	agentRoot := filepath.ToSlash(filepath.Join("chat-service", "internal", "agent"))
	appRoot := filepath.ToSlash(filepath.Join("chat-service", "internal", "app"))

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

		allowed := strings.HasPrefix(rel, agentRoot+"/") || strings.HasPrefix(rel, appRoot+"/")
		for _, imp := range imports {
			if imp != "github.com/cloudwego/eino" &&
				!strings.HasPrefix(imp, "github.com/cloudwego/eino/") {
				continue
			}
			if !allowed {
				t.Errorf("%s imports %q; Eino may only be used under %s or %s",
					rel, imp, agentRoot, appRoot)
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
