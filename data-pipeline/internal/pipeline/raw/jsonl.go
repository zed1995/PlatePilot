// Package raw defines the Google Local source schemas and a streaming reader for
// gzip JSONL files.
//
// The review file is 2.6 GB gzipped and 33M lines, so this package never reads
// a whole file into memory: callers iterate line by line and decode on demand.
package raw

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// maxLineBytes bounds a single JSONL line. Long review text and descriptions
// are orders of magnitude smaller, but bufio.Scanner's default 64KB limit is
// far too small and would silently truncate records.
const maxLineBytes = 16 << 20

// LineError reports a decode failure together with the source line number.
type LineError struct {
	Path   string
	LineNo int64
	Err    error
}

func (e *LineError) Error() string {
	return fmt.Sprintf("%s:%d: %v", e.Path, e.LineNo, e.Err)
}

// Unwrap exposes the underlying decode error.
func (e *LineError) Unwrap() error { return e.Err }

// Reader streams one JSON value per line from a gzip-compressed JSONL file.
type Reader struct {
	path    string
	file    *os.File
	gz      *gzip.Reader
	scanner *bufio.Scanner
	lineNo  int64
	err     error
	// counter measures compressed bytes read from the file. A gzip stream is
	// not seekable and the total line count is unknown up front, so
	// consumed/file-size is the only honest completion signal for a
	// multi-gigabyte import.
	counter *countingReader
}

// Open opens a gzip JSONL file for streaming.
func Open(path string) (*Reader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	// Counting the *compressed* side (the file, not the gzip stream) is what
	// makes progress meaningful: the on-disk size is known before the first
	// byte is decompressed, so consumed/size is a true completion ratio.
	counter := &countingReader{r: file}
	gz, err := gzip.NewReader(counter)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("open gzip %s: %w", path, err)
	}
	scanner := bufio.NewScanner(gz)
	scanner.Buffer(make([]byte, 0, 1<<20), maxLineBytes)
	return &Reader{path: path, file: file, gz: gz, scanner: scanner, counter: counter}, nil
}

// countingReader tracks how many compressed bytes have been pulled from the
// underlying file. gzip.NewReader reads its header eagerly, so the count is
// already above zero before the first Next call.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Next advances to the next non-empty line. It returns false at EOF or on a
// read error; call Err to distinguish.
func (r *Reader) Next() bool {
	for r.scanner.Scan() {
		r.lineNo++
		if len(bytes.TrimSpace(r.scanner.Bytes())) == 0 {
			continue
		}
		return true
	}
	r.err = r.scanner.Err()
	return false
}

// Bytes returns the current line.
func (r *Reader) Bytes() []byte { return r.scanner.Bytes() }

// LineNo returns the 1-based physical line number of the current line.
func (r *Reader) LineNo() int64 { return r.lineNo }

// Path returns the source path.
func (r *Reader) Path() string { return r.path }

// Err returns the terminal read error, if any.
func (r *Reader) Err() error { return r.err }

// Progress reports how far the read has advanced. total is the compressed file
// size in bytes; a zero total disables percentage reporting. Both values are
// snapshot copies, so a caller may print them from another goroutine.
func (r *Reader) Progress() (read, total int64) {
	if r.counter == nil {
		return 0, 0
	}
	if r.file != nil {
		if info, err := r.file.Stat(); err == nil {
			total = info.Size()
		}
	}
	return r.counter.n, total
}

// Decode unmarshals the current line into v.
func (r *Reader) Decode(v any) error {
	if err := json.Unmarshal(r.scanner.Bytes(), v); err != nil {
		return &LineError{Path: r.path, LineNo: r.lineNo, Err: err}
	}
	return nil
}

// Close releases the gzip stream and file.
func (r *Reader) Close() error {
	var errs []error
	if r.gz != nil {
		if err := r.gz.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.file != nil {
		if err := r.file.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// FileSHA256 returns the hex SHA-256 of a file, streamed so large files do not
// have to fit in memory.
func FileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
