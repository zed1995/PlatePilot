package admin_test

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestAdminApplicationLayerDoesNotImportDatabaseAdapter enforces the
// read-side boundary: the administration application layer coordinates the
// store through its port and must not name the concrete database adapter.
func TestAdminApplicationLayerDoesNotImportDatabaseAdapter(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("unquote import %s: %v", spec.Path.Value, err)
			}
			if strings.Contains(path, "shared/store/postgres") {
				t.Errorf("%s imports %s; the application layer must depend on "+
					"the store only through its port", name, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no source files were inspected")
	}
}
