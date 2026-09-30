package pipeline

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/domain/review"
)

// fakeSource drives the printer without a real gzip file.
type fakeSource struct {
	read, total int64
}

func (f *fakeSource) Progress() (int64, int64) { return f.read, f.total }

func TestProgressPrinterWritesCompletionRatio(t *testing.T) {
	var buf bytes.Buffer
	src := &fakeSource{read: 512 << 20, total: 1 << 30}
	p := newProgressPrinter("review", src, &buf)
	p.Tick(1234, ProgressSnapshot{Accepted: 900, Filtered: 300})
	p.Finish(1234, ProgressSnapshot{Accepted: 900, Filtered: 300})

	out := buf.String()
	if !strings.Contains(out, "[review]") {
		t.Errorf("missing stage label: %q", out)
	}
	if !strings.Contains(out, "50.00%") {
		t.Errorf("expected 50%% completion, got %q", out)
	}
	if !strings.Contains(out, "512.0MiB/1.0GiB") {
		t.Errorf("missing byte progress: %q", out)
	}
	if !strings.Contains(out, "rows=1,234") {
		t.Errorf("missing thousands separator on rows: %q", out)
	}
	if !strings.Contains(out, "accepted=900") {
		t.Errorf("missing accepted counter: %q", out)
	}
	if !strings.Contains(out, "done") {
		t.Errorf("final line should be marked done: %q", out)
	}
}

func TestProgressPrinterFirstTickHasNoRateOrETA(t *testing.T) {
	var buf bytes.Buffer
	p := newProgressPrinter("meta", &fakeSource{read: 1 << 20, total: 100 << 20}, &buf)
	p.Tick(10, ProgressSnapshot{})

	out := buf.String()
	// A rate derived from a zero baseline time is meaningless and previously
	// rendered as a negative ETA.
	if strings.Contains(out, "eta ") {
		t.Errorf("first tick must not report an ETA: %q", out)
	}
	if strings.Contains(out, "@") {
		t.Errorf("first tick must not report a rate: %q", out)
	}
}

func TestProgressPrinterThrottlesToByteDeltas(t *testing.T) {
	var buf bytes.Buffer
	src := &fakeSource{total: 1 << 30}
	p := newProgressPrinter("review", src, &buf)

	// 32MB is the minimum delta, so several sub-delta ticks must not print.
	for i := int64(1); i <= 10; i++ {
		src.read = i * (progressMinBytes / 10)
		p.Tick(i, ProgressSnapshot{})
	}
	writes := bytes.Count(buf.Bytes(), []byte("[review]"))
	if writes != 1 {
		t.Errorf("expected exactly 1 update below the byte threshold, got %d", writes)
	}
}

func TestProgressPrinterNilSourceIsNoop(t *testing.T) {
	var buf bytes.Buffer
	p := newProgressPrinter("meta", nil, &buf)
	p.Tick(1, ProgressSnapshot{})
	p.Finish(1, ProgressSnapshot{})
	if buf.Len() != 0 {
		t.Errorf("nil source should print nothing, got %q", buf.String())
	}
}

func TestProgressPrinterNilWriterIsNoop(t *testing.T) {
	p := newProgressPrinter("meta", &fakeSource{read: 1, total: 2}, nil)
	p.Tick(1, ProgressSnapshot{})
	p.Finish(1, ProgressSnapshot{}) // must not panic
}

func TestProgressPrinterFinishIsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	p := newProgressPrinter("meta", &fakeSource{read: 1 << 20, total: 1 << 20}, &buf)
	p.Finish(1, ProgressSnapshot{})
	first := buf.Len()
	p.Finish(1, ProgressSnapshot{})
	if buf.Len() != first {
		t.Error("Finish must not print twice")
	}
}

func TestProgressPrinterZeroTotalOmitsPercent(t *testing.T) {
	var buf bytes.Buffer
	p := newProgressPrinter("meta", &fakeSource{read: 42, total: 0}, &buf)
	p.Tick(1, ProgressSnapshot{})
	if strings.Contains(buf.String(), "%") {
		t.Errorf("unknown total should not print a percentage: %q", buf.String())
	}
}

func TestProgressPrinterClampsPercentAt100(t *testing.T) {
	var buf bytes.Buffer
	// A reader can overshoot slightly when the final block is read.
	p := newProgressPrinter("meta", &fakeSource{read: 100, total: 99}, &buf)
	p.Finish(1, ProgressSnapshot{})
	if !strings.Contains(buf.String(), "100.00%") {
		t.Errorf("overshoot should clamp to 100%%: %q", buf.String())
	}
}

func TestProgressPrinterUsesCarriageReturnForLiveLines(t *testing.T) {
	var buf bytes.Buffer
	src := &fakeSource{total: 1 << 30}
	p := newProgressPrinter("meta", src, &buf)
	src.read = progressMinBytes + 1
	p.Tick(1, ProgressSnapshot{})
	if !strings.Contains(buf.String(), "\r") {
		t.Errorf("live updates should redraw in place: %q", buf.String())
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1.0KiB"},
		{1536, "1.5KiB"},
		{1 << 20, "1.0MiB"},
		{1 << 30, "1.0GiB"},
		{3 << 30, "3.0GiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestHumanCount(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{7, "7"},
		{999, "999"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
		{123456789, "123,456,789"},
	}
	for _, c := range cases {
		if got := humanCount(c.in); got != c.want {
			t.Errorf("humanCount(%d) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0s"},
		{45, "45s"},
		{90, "1m30s"},
		{3600, "1h00m"},
		{7500, "2h05m"},
	}
	for _, c := range cases {
		if got := humanDuration(c.in); got != c.want {
			t.Errorf("humanDuration(%v) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestProgressSnapshotFromBatchReport(t *testing.T) {
	// progressSnapshot must carry every counter the line renders.
	got := progressSnapshot(batchReportFixture())
	want := ProgressSnapshot{Accepted: 10, Written: 8, Deduped: 2, Filtered: 5, Rejected: 1, Unmatched: 3}
	if got != want {
		t.Errorf("progressSnapshot = %+v want %+v", got, want)
	}
}

// TestProgressIntervalIsSane guards the tuning constants: a run that updates
// every 32MB of a 2.5GB file should refresh roughly every 100 times, and never
// faster than the 2s floor when bytes stall.
func TestProgressIntervalIsSane(t *testing.T) {
	if progressMinBytes <= 0 {
		t.Error("progressMinBytes must be positive")
	}
	if progressMinInterval < time.Second {
		t.Errorf("progressMinInterval = %v, want >= 1s", progressMinInterval)
	}
	if progressMinBytes < 1<<20 {
		t.Errorf("progressMinBytes = %d, want >= 1MiB", progressMinBytes)
	}
}

// batchReportFixture builds a report with known counters.
func batchReportFixture() review.BatchReport {
	return review.BatchReport{
		Accepted:  10,
		Written:   8,
		Deduped:   2,
		Filtered:  5,
		Rejected:  1,
		Unmatched: 3,
	}
}
