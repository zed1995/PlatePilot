package pipeline

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Progress reporting for the streaming import stages.
//
// A full-corpus run reads a 2.5 GB gzipped review file line by line for tens of
// minutes with no output at all, which is indistinguishable from a hang. The
// reporter prints a single rewritten line per update, carrying the counters
// that explain where the rows are going: read bytes (real completion ratio),
// rows, and the accept/filter/reject breakdown.
//
// Updates are driven by byte progress rather than a timer so a fast stage
// cannot flood the terminal, and rate and ETA stay meaningful when throughput
// varies across the file.
const (
	// progressMinBytes is the compressed-byte delta between two updates.
	progressMinBytes = 32 << 20
	// progressMinInterval bounds how often a line may be rewritten even when
	// bytes are advancing quickly.
	progressMinInterval = 2 * time.Second
	// progressResume is the terminal value: the reader has consumed the whole
	// file (or stopped early at --limit), so no further line is printed.
	progressResume = "\r\x1b[2K"
)

// progressSource is the subset of raw.Reader the reporter needs. Keeping it an
// interface lets tests drive the reporter without a real gzip file.
type progressSource interface {
	// Progress returns compressed bytes read and the total file size.
	Progress() (read, total int64)
}

// progressPrinter renders a one-line status for an import stage.
type progressPrinter struct {
	stage   string
	source  progressSource
	out     io.Writer
	mu      sync.Mutex
	started bool
	lastAt  time.Time
	lastRaw int64
	// lastTotal caches the file size so the ratio can be computed even if Stat
	// later fails.
	lastTotal int64
	stopped   bool
}

// newProgressPrinter returns a printer for one stage, writing to out.
func newProgressPrinter(stage string, source progressSource, out io.Writer) *progressPrinter {
	return &progressPrinter{stage: stage, source: source, out: out}
}

// Tick reports progress if enough compressed bytes or time have elapsed. It is
// safe to call once per row read; most calls do no output.
func (p *progressPrinter) Tick(rowsRead int64, report ProgressSnapshot) {
	if p == nil || p.source == nil {
		return
	}
	read, total := p.source.Progress()
	if total > 0 {
		p.lastTotal = total
	} else {
		total = p.lastTotal
	}
	if read == p.lastRaw && read < total {
		return // no movement since the last update
	}
	now := time.Now()
	if read-p.lastRaw < progressMinBytes && now.Sub(p.lastAt) < progressMinInterval {
		return
	}
	// The first update has no baseline to measure against, so it reports no
	// rate and no ETA rather than a bogus one derived from a zero timestamp.
	rate := 0.0
	if p.started {
		if elapsed := now.Sub(p.lastAt); elapsed > 0 && read > p.lastRaw {
			rate = float64(read-p.lastRaw) / elapsed.Seconds()
		}
	}
	p.started = true
	p.lastAt = now
	p.lastRaw = read
	p.write(now, read, total, rowsRead, report, rate)
}

// Finish prints a final non-transient line so the last state survives in logs
// and piped output.
func (p *progressPrinter) Finish(rowsRead int64, report ProgressSnapshot) {
	if p == nil || p.source == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.stopped = true
	read, total := p.source.Progress()
	if total == 0 {
		total = p.lastTotal
	}
	p.writeLine(p.format(time.Now(), read, total, rowsRead, report, 0, true), true)
}

func (p *progressPrinter) write(now time.Time, read, total, rowsRead int64, report ProgressSnapshot, rate float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.writeLine(p.format(now, read, total, rowsRead, report, rate, false), false)
}

// writeLine emits an in-place status line that overwrites the previous one.
// Transient updates use a carriage return; the final line is terminated so it
// survives piped output and log capture.
func (p *progressPrinter) writeLine(line string, final bool) {
	if p.out == nil {
		return
	}
	if final {
		_, _ = io.WriteString(p.out, progressResume+line+"\n")
		return
	}
	_, _ = io.WriteString(p.out, "\r\x1b[2K"+line)
}

func (p *progressPrinter) format(now time.Time, read, total, rowsRead int64, report ProgressSnapshot, rate float64, final bool) string {
	var b strings.Builder
	// A percentage with an unknown denominator is worse than none: "0.00%"
	// reads as "nothing happened" rather than "size unavailable", so the
	// percent field is omitted entirely when the total is zero.
	if total > 0 {
		pct := float64(read) / float64(total) * 100
		if pct > 100 {
			pct = 100
		}
		fmt.Fprintf(&b, "[%s] %6.2f%%  ", p.stage, pct)
	} else {
		fmt.Fprintf(&b, "[%s] %6s  ", p.stage, "--")
	}
	fmt.Fprintf(&b, "%s/%s", humanBytes(read), humanBytes(total))
	if rate > 0 {
		fmt.Fprintf(&b, "  @%s/s", humanBytes(int64(rate)))
		if total > 0 && rate > 0 {
			remaining := float64(total-read) / rate
			if remaining >= 0 {
				fmt.Fprintf(&b, "  eta %s", humanDuration(remaining))
			}
		}
	}
	fmt.Fprintf(&b, "  rows=%s", humanCount(rowsRead))
	fmt.Fprintf(&b, "  accepted=%s written=%s dedup=%s filtered=%s rejected=%s unmatched=%s",
		humanCount(report.Accepted), humanCount(report.Written), humanCount(report.Deduped),
		humanCount(report.Filtered), humanCount(report.Rejected), humanCount(report.Unmatched))
	if final {
		b.WriteString("  done")
	}
	return b.String()
}

// ProgressSnapshot is the subset of the batch counters worth showing live.
// A full review.BatchReport is too wide for one terminal line.
type ProgressSnapshot struct {
	Accepted  int64
	Written   int64
	Deduped   int64
	Filtered  int64
	Rejected  int64
	Unmatched int64
}

// humanBytes formats a byte count with binary units.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGT"[exp])
}

// humanCount formats a large count with thousands separators.
func humanCount(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i, ch := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, ch)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// humanDuration formats seconds as a compact h/m/s string.
func humanDuration(seconds float64) string {
	if seconds < 0 || seconds != seconds { // NaN guard
		return "?"
	}
	d := time.Duration(seconds) * time.Second
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
