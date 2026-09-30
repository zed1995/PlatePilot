package raw

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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

func TestProgressReportsCompressedByteRatio(t *testing.T) {
	// Many rows, so the reader must advance well past the gzip header before
	// the first row is yielded.
	lines := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		lines = append(lines, `{"gmap_id":"g`+strings.Repeat("x", 200)+`","n":`+strconv.Itoa(i)+`}`)
	}
	path := filepath.Join(t.TempDir(), "many.json.gz")
	writeGz(t, path, lines)

	reader, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()

	read, total := reader.Progress()
	if total <= 0 {
		t.Fatalf("total = %d, want the file size", total)
	}
	if read <= 0 || read > total {
		t.Fatalf("read = %d, want 0 < read <= total (%d)", read, total)
	}

	// Drain the file: progress must be monotonic and end at the full size.
	prev := read
	for reader.Next() {
		var record Meta
		if err := reader.Decode(&record); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if reader.LineNo()%100 != 0 {
			continue
		}
		cur, curTotal := reader.Progress()
		if cur < prev {
			t.Fatalf("progress went backwards: %d then %d", prev, cur)
		}
		if curTotal != total {
			t.Fatalf("total changed mid-stream: %d then %d", total, curTotal)
		}
		prev = cur
	}
	read, total = reader.Progress()
	if read != total {
		t.Errorf("after EOF read = %d, want total = %d", read, total)
	}
}

func TestProgressOnEmptyGzipFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json.gz")
	writeGz(t, path, nil)

	reader, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()
	if reader.Next() {
		t.Error("empty file should yield no rows")
	}
	read, total := reader.Progress()
	if total <= 0 {
		t.Errorf("total = %d, want > 0", total)
	}
	if read != total {
		t.Errorf("read = %d, want total = %d on an empty stream", read, total)
	}
}

func TestReviewDecodesPicsInSourceShape(t *testing.T) {
	// The source spells pics as [{"url": ["https://..."]}]. A typed []string
	// field made every review carrying a photo fail to decode, which silently
	// dropped ~2.4% of the corpus at the import boundary.
	line := `{"gmap_id":"a","time":1614600000000,"rating":5,"pics":[{"url":["https://example.com/p.jpg"]}]}`
	var record Review
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("pics in the source shape must decode: %v", err)
	}
	if record.GmapID != "a" || record.Rating != 5 {
		t.Errorf("record = %+v", record)
	}
	if len(record.Pics) == 0 {
		t.Error("pics should be preserved as raw JSON")
	}
}

func TestReviewDecodesAlternatePicsShapes(t *testing.T) {
	// Defensive: other shapes must not fail the whole record either.
	for _, line := range []string{
		`{"gmap_id":"a","pics":null}`,
		`{"gmap_id":"a","pics":[]}`,
		`{"gmap_id":"a","pics":["https://example.com/x.jpg"]}`,
		`{"gmap_id":"a","pics":{"url":"https://example.com/x.jpg"}}`,
		`{"gmap_id":"a"}`,
	} {
		var record Review
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Errorf("line %s: %v", line, err)
		}
	}
}

func TestReviewOmitsRawIdentityFieldsWhenCleared(t *testing.T) {
	// A nil json.RawMessage marshals as the literal "null", so omitempty is the
	// only thing that keeps pics and resp out of the written record.
	record := Review{GmapID: "a", Rating: 5}
	out, err := json.Marshal(&record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(out)
	for _, field := range []string{"pics", "resp"} {
		if strings.Contains(text, field) {
			t.Errorf("cleared field %q survived: %s", field, text)
		}
	}
	// The string identity fields carry no data once cleared, which is the
	// property that matters: no reviewer identity reaches the curated layer.
	var round Review
	if err := json.Unmarshal(out, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round.UserID != "" || round.Name != "" {
		t.Errorf("identity leaked: user_id=%q name=%q", round.UserID, round.Name)
	}
}
