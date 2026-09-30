package pipeline

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHintGz writes a one-line gzip JSONL file, enough to make the corpus
// exist on disk for the not-found hint to detect.
func writeHintGz(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	if _, err := gz.Write([]byte(`{"gmap_id":"a"}\n`)); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

func TestNotFoundHintNamesThePrefilteredCorpus(t *testing.T) {
	// A bare "no such file" made the operator guess; the hint must name the file
	// that does exist and the flags that read it.
	dir := t.TempDir()
	writeHintGz(t, filepath.Join(dir, DefaultFilteredReviewFile))

	hint := notFoundHint(dir, ReviewFileName)
	if hint == "" {
		t.Fatal("expected a hint when a prefiltered corpus is present")
	}
	for _, want := range []string{DefaultFilteredReviewFile, "--review-file"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q should mention %q", hint, want)
		}
	}
	// An explicit override that is genuinely missing must not mislead.
	if got := notFoundHint(dir, "some-other.json.gz"); got != "" {
		t.Errorf("hint for an explicit filename = %q, want empty", got)
	}
	// No prefiltered file present means there is nothing useful to say.
	if got := notFoundHint(t.TempDir(), ReviewFileName); got != "" {
		t.Errorf("hint with no prefiltered corpus = %q, want empty", got)
	}
}
