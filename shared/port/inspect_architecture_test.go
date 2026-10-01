package port_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestInspectStoreHasNoWriteMethods enforces the read-only rule at the type
// level: the administration console's query surface must not name any method
// that looks like a mutation. The interface is parsed from source so the rule
// is checked against what developers actually declared.
func TestInspectStoreHasNoWriteMethods(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "inspect.go", nil, 0)
	if err != nil {
		t.Fatalf("parse inspect.go: %v", err)
	}

	// writePrefixes are the name prefixes that signal a mutation.
	writePrefixes := []string{
		"Create", "Update", "Delete", "Upsert", "Insert",
		"Exec", "Write", "Put", "Save",
	}

	var found bool
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "InspectStore" {
				continue
			}
			found = true
			iface, ok := ts.Type.(*ast.InterfaceType)
			if !ok {
				t.Fatal("InspectStore is not an interface type")
			}
			for _, method := range iface.Methods.List {
				if len(method.Names) == 0 {
					// An embedded interface carries no name of its own here.
					continue
				}
				name := method.Names[0].Name
				for _, prefix := range writePrefixes {
					if strings.HasPrefix(name, prefix) {
						t.Errorf("InspectStore.%s starts with %q and looks like a "+
							"write method; the console is strictly read-only", name, prefix)
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("InspectStore interface was not found in inspect.go")
	}
}
