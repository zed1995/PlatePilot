package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zed/platepilot/data-pipeline/internal/pipeline/curate"
)

// The meta stage must label restaurants from real geometry when it is
// available, and must fall back rather than fail when it is not.
func TestLoadBoroughsPolicy(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.geojson")

	b, err := loadBoroughs(ImportOptions{BoundaryFile: missing})
	if err != nil || b != nil {
		t.Errorf("missing file without require: b=%v err=%v (want nil, nil)", b, err)
	}

	b, err = loadBoroughs(ImportOptions{BoundaryFile: missing, RequireBoundaries: true})
	if err == nil || b != nil {
		t.Errorf("missing file with require: b=%v err=%v (want nil, error)", b, err)
	} else if !strings.Contains(err.Error(), missing) {
		t.Errorf("error does not name the missing file: %v", err)
	}

	b, err = loadBoroughs(ImportOptions{BoundaryFile: "", RequireBoundaries: true})
	if err == nil {
		t.Errorf("empty path with require: expected an error, got b=%v", b)
	}

	// A file that exists but is not the pinned geometry.
	bad := filepath.Join(dir, "bad.geojson")
	if err := os.WriteFile(bad, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = loadBoroughs(ImportOptions{BoundaryFile: bad}); err != nil {
		t.Errorf("corrupt file without require should fall back, got %v", err)
	}
	if _, err = loadBoroughs(ImportOptions{BoundaryFile: bad, RequireBoundaries: true}); err == nil {
		t.Error("corrupt file with require: expected an error")
	}

	// The real geometry loads through the default repo-relative path. That file
	// is git-ignored, so a fresh checkout legitimately does not have it; the
	// geometry itself is covered by the curate package, which skips the same
	// way.
	real, err := loadBoroughs(ImportOptions{BoundaryFile: curate.DefaultBoundaryFile})
	if err != nil {
		t.Fatalf("real geometry: %v", err)
	}
	if real == nil {
		t.Skip("boundary geometry not present; see data/boundaries/README.md")
	}
	if got := real.BoroughAt(40.7580, -73.9855); got != "manhattan" {
		t.Errorf("Times Square = %q", got)
	}
}
