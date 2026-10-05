package domain_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// domainImportAllowPrefix is the only non-stdlib import prefix the domain layer
// may use: the domain may depend on itself and nothing else.
const domainImportAllowPrefix = "github.com/zed1995/platepilot/shared/domain"

// TestDomainLayerHasNoFrameworkOrVendorDependencies enforces the PRD's rule that
// the domain layer depends on no database driver, Eino, Hertz, Ollama, or vendor
// SDK. It is what keeps a change of storage engine cheap: the 798 lines of
// domain DTOs never name a storage engine.
func TestDomainLayerHasNoFrameworkOrVendorDependencies(t *testing.T) {
	var checked int
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		imports, parseErr := parseImports(path)
		if parseErr != nil {
			return parseErr
		}
		checked++
		for _, imp := range imports {
			if isStdlib(imp) || strings.HasPrefix(imp, domainImportAllowPrefix) {
				continue
			}
			t.Errorf("%s imports %q; the domain layer may only depend on the standard library and %s",
				path, imp, domainImportAllowPrefix)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk domain packages: %v", err)
	}
	if checked == 0 {
		t.Fatal("no domain source files were inspected")
	}
}

func parseImports(path string) ([]string, error) {
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

// isStdlib reports whether an import path looks like a standard library package:
// standard import paths have no dot in their first path element.
func isStdlib(importPath string) bool {
	first := importPath
	if idx := strings.Index(importPath, "/"); idx >= 0 {
		first = importPath[:idx]
	}
	return !strings.Contains(first, ".")
}

// TestServicesDoNotDependOnEachOther enforces the repository layout rule from the
// implementation plan §3.2: data-pipeline and chat-service share code only via
// shared/, never by importing each other.
func TestServicesDoNotDependOnEachOther(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	for _, dir := range []string{"shared", "chat-service", "data-pipeline"} {
		if _, statErr := os.Stat(filepath.Join(root, dir)); statErr != nil {
			t.Skipf("repository layout not found at %s: %v", root, statErr)
		}
	}

	rules := []struct {
		service      string
		otherService string
	}{
		{"data-pipeline", "github.com/zed1995/platepilot/chat-service"},
		{"chat-service", "github.com/zed1995/platepilot/data-pipeline"},
	}
	for _, rule := range rules {
		base := filepath.Join(root, rule.service)
		walkErr := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			imports, parseErr := parseImports(path)
			if parseErr != nil {
				return parseErr
			}
			for _, imp := range imports {
				if strings.HasPrefix(imp, rule.otherService) {
					t.Errorf("%s imports %s; the two services must share code only via shared/", path, imp)
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", base, walkErr)
		}
	}
}

// TestDomainLayerDoesNotNameAStorageEngine is a narrower, more legible guard
// than the import check above: the domain must not even mention the storage
// engine by name in a comment or a type, because storage vocabulary is how a
// backend starts shaping a supposedly backend-neutral layer.
func TestDomainLayerDoesNotNameAStorageEngine(t *testing.T) {
	banned := []string{"postgres", "postgis", "pgvector", "sql"}
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		// This file names every banned term in order to search for them, so
		// scanning it would always fail.
		if strings.HasSuffix(path, "architecture_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lower := strings.ToLower(string(body))
		for _, term := range banned {
			if strings.Contains(lower, term) {
				t.Errorf("%s mentions %q; the domain layer must stay storage-agnostic", path, term)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk domain packages: %v", err)
	}
}
