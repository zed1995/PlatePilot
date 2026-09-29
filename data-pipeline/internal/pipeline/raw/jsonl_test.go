package raw

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGz(t *testing.T, path string, lines []string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	for _, line := range lines {
		if _, err := gz.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write gz: %v", err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gz: %v", err)
	}
}

func TestReaderStreamsLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.json.gz")
	writeGz(t, path, []string{
		`{"gmap_id":"a","name":"A"}`,
		``, // blank lines are skipped but still advance the line counter
		`{"gmap_id":"b","name":"B"}`,
	})

	reader, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()

	var ids []string
	var lines []int64
	for reader.Next() {
		var record Meta
		if err := reader.Decode(&record); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		ids = append(ids, record.GmapID)
		lines = append(lines, reader.LineNo())
	}
	if err := reader.Err(); err != nil {
		t.Fatalf("Err: %v", err)
	}
	if strings.Join(ids, ",") != "a,b" {
		t.Errorf("ids = %v", ids)
	}
	if lines[0] != 1 || lines[1] != 3 {
		t.Errorf("line numbers = %v want [1 3]", lines)
	}
}

func TestReaderDecodesLongLines(t *testing.T) {
	long := strings.Repeat("x", 200_000) // far beyond the default 64KB scanner limit
	path := filepath.Join(t.TempDir(), "long.json.gz")
	writeGz(t, path, []string{`{"gmap_id":"a","description":"` + long + `"}`})

	reader, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()
	if !reader.Next() {
		t.Fatalf("Next: %v", reader.Err())
	}
	var record Meta
	if err := reader.Decode(&record); err != nil {
		t.Fatalf("Decode long line: %v", err)
	}
	if record.Description == nil || len(*record.Description) != 200_000 {
		t.Errorf("long description truncated: %d chars", len(deref(record.Description)))
	}
}

func TestReaderReportsLineErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json.gz")
	writeGz(t, path, []string{`{"gmap_id":"a"}`, `{"gmap_id":`})

	reader, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()

	if !reader.Next() {
		t.Fatal("first line should decode")
	}
	var record Meta
	if err := reader.Decode(&record); err != nil {
		t.Fatalf("first Decode: %v", err)
	}
	if !reader.Next() {
		t.Fatal("second line should be present")
	}
	err = reader.Decode(&record)
	var lineErr *LineError
	if !errors.As(err, &lineErr) {
		t.Fatalf("want *LineError, got %v", err)
	}
	if lineErr.LineNo != 2 {
		t.Errorf("LineError.LineNo = %d want 2", lineErr.LineNo)
	}
}

func TestFileSHA256IsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.json.gz")
	writeGz(t, path, []string{`{"gmap_id":"a"}`})

	first, err := FileSHA256(path)
	if err != nil {
		t.Fatalf("FileSHA256: %v", err)
	}
	second, err := FileSHA256(path)
	if err != nil {
		t.Fatalf("FileSHA256: %v", err)
	}
	if first != second || len(first) != 64 {
		t.Errorf("hashes = %q / %q", first, second)
	}
}

func TestOpenMissingFile(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope.json.gz")); err == nil {
		t.Error("opening a missing file should fail")
	}
}

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func TestMISCUnmarshalIsLenient(t *testing.T) {
	var record Meta
	line := `{"gmap_id":"a","MISC":{"Service options":["Takeout"],"Atmosphere":"Casual","Weird":{"a":1}}}`
	if err := jsonUnmarshal([]byte(line), &record); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := record.MISC["Service options"]; len(got) != 1 || got[0] != "Takeout" {
		t.Errorf("Service options = %v", got)
	}
	if got := record.MISC["Atmosphere"]; len(got) != 1 || got[0] != "Casual" {
		t.Errorf("Atmosphere = %v", got)
	}
	if got := record.MISC["Weird"]; len(got) != 0 {
		t.Errorf("unsupported MISC shape should yield no labels, got %v", got)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
