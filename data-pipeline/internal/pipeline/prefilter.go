package pipeline

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zed/platepilot/data-pipeline/internal/pipeline/curate"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline/raw"
	"github.com/zed/platepilot/shared/domain/errs"
)

// prefilterSentinelID stands in for a resolved restaurant id when prefilter
// reuses the importer's review validation without performing the join.
const prefilterSentinelID int64 = 1

// DefaultProcessedDir is where generated, git-ignored corpora are written.
const DefaultProcessedDir = "data/processed"

// DefaultFilteredReviewFile is the output name inside the processed directory.
const DefaultFilteredReviewFile = "review-filtered.json.gz"

// prefilterBatchSize is how many reviews are buffered before writing.
const prefilterBatchSize = 2000

// PrefilterOptions controls one prefilter run.
type PrefilterOptions struct {
	// DataDir holds the raw gz files.
	DataDir string
	// OutDir receives the filtered corpus. Empty uses DefaultProcessedDir.
	OutDir string
	// OutFile is the filtered file name. Empty uses DefaultFilteredReviewFile.
	OutFile string
	// MinTextChars is the shortest scrubbed text kept. A review with no text, or
	// with text shorter than this, is dropped entirely rather than stored as a
	// rating-only row.
	MinTextChars int
	// KeepShortText keeps sub-threshold text with the text blanked, which is the
	// behaviour the M1 importer applies. By default such rows are dropped,
	// because a review with no usable text is not evidence.
	KeepShortText bool
	// Limit bounds rows read from the source. Zero reads the whole file.
	Limit int
	// ProgressOut receives the live progress line. Nil disables it.
	ProgressOut io.Writer
}

// PrefilterResult summarises a prefilter run.
type PrefilterResult struct {
	SourceFile  string
	OutputFile  string
	OutputBytes int64
	RowsRead    int64
	// Kept is the number of reviews written to the output file.
	Kept int64
	// Unmatched is the number of reviews whose gmap_id is not in the restaurant
	// set. These can never be imported, at any later time.
	Unmatched int64
	// NoText is the number of joinable reviews with no text field at all.
	NoText int64
	// ShortText is the number of joinable reviews whose scrubbed text fell below
	// the threshold.
	ShortText int64
	// Rejected is the number of structurally invalid reviews.
	Rejected int64
	// RestaurantsAvailable is the size of the joinable id set used for the run.
	RestaurantsAvailable int
	// RestaurantsCovered is how many distinct restaurants survive filtering.
	RestaurantsCovered int
	DurationMS         int64
}

// KeptFraction returns the share of source rows written to the output.
func (r PrefilterResult) KeptFraction() float64 {
	if r.RowsRead == 0 {
		return 0
	}
	return float64(r.Kept) / float64(r.RowsRead)
}

// Prefilter streams the raw review corpus and writes a smaller one containing
// only the reviews that are joinable to an ingested restaurant and carry usable
// text.
//
// It exists because the shipped review file is 2.5 GB in which roughly 74% of
// rows reference places outside the ingested service area and can never join to
// a restaurant, and roughly half of the rest have no text at all. Pushing that
// filter locally removes tens of millions of rows from the database join and write
// path, which is the difference between a multi-hour import and a short one.
//
// The curation rules come from the same curate package the importer uses, so a
// row that survives prefilter is exactly a row the importer would keep: there is
// no second, divergent definition of "usable".
//
// The output is a gzip JSONL file in the raw schema, so
// `import --data-dir=<outdir>` consumes it unchanged.
func Prefilter(ctx context.Context, stores Stores, opts PrefilterOptions) (PrefilterResult, error) {
	if stores.Restaurants == nil {
		return PrefilterResult{}, errs.New(errs.CodeInvalidArgument,
			"prefilter needs the restaurant set to decide which reviews are joinable; set POSTGRES_DSN")
	}
	if strings.TrimSpace(opts.DataDir) == "" {
		return PrefilterResult{}, errs.New(errs.CodeInvalidArgument, "prefilter needs a data dir")
	}
	if opts.OutDir == "" {
		opts.OutDir = DefaultProcessedDir
	}
	if opts.OutFile == "" {
		opts.OutFile = DefaultFilteredReviewFile
	}
	if opts.MinTextChars < 0 {
		opts.MinTextChars = 0
	}

	// Resolve the whole joinable id set up front, in one projection-only query.
	// Resolving ids row by row would mean tens of millions of point queries,
	// which is the exact cost this stage exists to avoid.
	ids, err := stores.Restaurants.ListSourceRecordIDs(ctx)
	if err != nil {
		return PrefilterResult{}, err
	}
	known := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		known[id] = struct{}{}
	}

	srcPath := filepath.Join(opts.DataDir, ReviewFileName)
	outPath := filepath.Join(opts.OutDir, opts.OutFile)
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return PrefilterResult{}, errs.Wrap(errs.CodeInvalidArgument,
			"prefilter: create output dir "+opts.OutDir, err)
	}

	reader, err := raw.Open(srcPath)
	if err != nil {
		return PrefilterResult{}, errs.Wrap(errs.CodeNotFound, "prefilter: open "+srcPath, err)
	}
	defer reader.Close()

	// Write to a temp file and rename on success, so an interrupted run never
	// leaves a partial corpus that a later import would silently consume.
	tmpPath := outPath + ".partial"
	out, err := os.Create(tmpPath)
	if err != nil {
		return PrefilterResult{}, errs.Wrap(errs.CodeInvalidArgument, "prefilter: create "+tmpPath, err)
	}
	cleanup := func() {
		_ = out.Close()
		_ = os.Remove(tmpPath) // no-op once the rename succeeded
	}
	defer cleanup()

	gz := gzip.NewWriter(out)
	buf := bufio.NewWriterSize(gz, 1<<20)

	progress := newProgressPrinter("prefilter", reader, opts.ProgressOut)
	result := PrefilterResult{SourceFile: srcPath, OutputFile: outPath, RestaurantsAvailable: len(known)}
	defer func() { progress.Finish(result.RowsRead, prefilterSnapshot(result)) }()

	reviewOpts := curate.ReviewOptions{MinTextChars: opts.MinTextChars, ObservedAt: SnapshotObservedAt}
	threshold := maxInt(reviewOpts.MinTextChars, 1)
	covered := make(map[string]struct{})

	batch := make([]raw.Review, 0, prefilterBatchSize)
	flush := func() error {
		for i := range batch {
			line, err := json.Marshal(&batch[i])
			if err != nil {
				return errs.Wrap(errs.CodeInternal, "prefilter: marshal review", err)
			}
			if _, err := buf.Write(line); err != nil {
				return errs.Wrap(errs.CodeInternal, "prefilter: write review", err)
			}
			if err := buf.WriteByte('\n'); err != nil {
				return errs.Wrap(errs.CodeInternal, "prefilter: write newline", err)
			}
		}
		batch = batch[:0]
		return nil
	}

	started := time.Now()
	for reader.Next() {
		if opts.Limit > 0 && result.RowsRead >= int64(opts.Limit) {
			break
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.RowsRead++
		progress.Tick(result.RowsRead, prefilterSnapshot(result))

		var record raw.Review
		if err := reader.Decode(&record); err != nil {
			result.Rejected++
			continue
		}

		// A review is only importable if its place was ingested. Filtering here is
		// the single biggest saving: doomed rows never reach the database join.
		if _, ok := known[record.GmapID]; !ok {
			result.Unmatched++
			continue
		}

		// Decide on the text before anything else. A review with no usable text
		// is not evidence, and the corpus does not need it to score a place.
		scrubbed := ""
		switch {
		case record.Text == nil || strings.TrimSpace(*record.Text) == "":
			result.NoText++
			if !opts.KeepShortText {
				continue
			}
		default:
			scrubbed = curate.ScrubPII(strings.TrimSpace(*record.Text))
			if len([]rune(scrubbed)) < threshold {
				result.ShortText++
				if !opts.KeepShortText {
					continue
				}
			}
		}

		// Reuse the importer's own validation so prefilter cannot drift from it.
		// The restaurant id is a non-zero sentinel: prefilter runs before the
		// join and only needs the shared rules, never a real foreign key.
		if _, err := curate.NormalizeReview(record, prefilterSentinelID, reviewOpts); err != nil {
			result.Rejected++
			continue
		}

		result.Kept++
		covered[record.GmapID] = struct{}{}
		// Store the scrubbed text so PII is already gone from the intermediate
		// file, and drop the identity fields the curated layer must never keep.
		record.Text = &scrubbed
		record.UserID, record.Name = "", ""
		// Pics and Resp carry omitempty, so clearing the slices drops both the
		// "null" literal and the empty value from the written record.
		record.Pics, record.Resp = nil, nil
		batch = append(batch, record)
		if len(batch) >= cap(batch) {
			if err := flush(); err != nil {
				return result, err
			}
			progress.Tick(result.RowsRead, prefilterSnapshot(result))
		}
	}
	if err := reader.Err(); err != nil {
		return result, errs.Wrap(errs.CodeInternal, "prefilter: read "+srcPath, err)
	}
	if err := flush(); err != nil {
		return result, err
	}
	if err := buf.Flush(); err != nil {
		return result, errs.Wrap(errs.CodeInternal, "prefilter: flush buffer", err)
	}
	if err := gz.Close(); err != nil {
		return result, errs.Wrap(errs.CodeInternal, "prefilter: close gzip", err)
	}
	if err := out.Close(); err != nil {
		return result, errs.Wrap(errs.CodeInternal, "prefilter: close file", err)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		return result, errs.Wrap(errs.CodeInternal, "prefilter: rename into place", err)
	}
	if info, err := os.Stat(outPath); err == nil {
		result.OutputBytes = info.Size()
	}
	result.RestaurantsCovered = len(covered)
	result.DurationMS = time.Since(started).Milliseconds()
	return result, nil
}

func prefilterSnapshot(r PrefilterResult) ProgressSnapshot {
	return ProgressSnapshot{
		Accepted:  r.Kept,
		Unmatched: r.Unmatched,
		Rejected:  r.Rejected,
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
