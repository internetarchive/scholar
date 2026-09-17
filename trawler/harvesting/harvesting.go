// harvesting contains common functions and structs for querying upstream data
// sources (like crossref) and handling the resulting piles of ndjson.
package harvesting

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"git.archive.org/webgroup/scholar/trawler/s3"
	"go.temporal.io/sdk/activity"
)

type FindLineBatchInput struct {
	S3Key     string
	Offset    int64
	BatchSize int
	ChunkSize int
}

type FindLineBatchOutput struct {
	Offsets   [][]int64
	BytesRead int64
	EOF       bool
}

// FindLineBatch does a range read over an S3-stored pile of text (probably
// ndjson) and returns a list of offsets at which lines can be found. It
// reports if it hit the end of the file as well as how many bytes it read.
// This is intended to be run in parallel so a very large file can have line
// offsets extracted from different regions of the file at the same time.
func FindLineBatch(ctx context.Context, in FindLineBatchInput) (FindLineBatchOutput, error) {
	if in.BatchSize == 0 || in.ChunkSize == 0 {
		panic("bad batch or chunk size config")
	}

	l := activity.GetLogger(ctx)

	l.Info(fmt.Sprintf("doing a range read from '%s'", in.S3Key))

	f, err := s3.GetObject(ctx, in.S3Key)
	if err != nil {
		return FindLineBatchOutput{}, err
	}
	defer f.Close()

	cc := chunkCfg{
		Offset:    in.Offset,
		BatchSize: in.BatchSize,
		ChunkSize: in.ChunkSize,
	}

	out, err := chunk(cc, f)
	if err != nil {
		return out, fmt.Errorf("chunking of %s failed: %w", in.S3Key, err)
	}

	return out, nil
}

type chunkCfg struct {
	Offset    int64
	BatchSize int
	ChunkSize int
}

// chunk is a helper function split out for unit testing. It's critical to
// remember that its BytesRead value is cumulative -- ie, it's bytes read
// *including* the initial offset value. This is quite confusing and I may
// change it...
//
// EOF means "the scan reached the end of the file", not merely "the last read
// touched the end of the file". Filling BatchSize part-way through a buffer
// leaves bytes unscanned, and callers treat EOF as "this object is done" and
// move on -- so reporting EOF there would silently abandon the rest of the
// object. In that case EOF stays false and the caller resumes from BytesRead.
func chunk(cc chunkCfg, r io.ReaderAt) (out FindLineBatchOutput, err error) {
	out = FindLineBatchOutput{
		BytesRead: cc.Offset,
	}
	curLineStart := cc.Offset

	var curLineLength int64

	for {
		b := make([]byte, cc.ChunkSize)
		bufStart := out.BytesRead
		n, err := r.ReadAt(b, bufStart)
		atEnd := errors.Is(err, io.EOF)
		if atEnd {
			err = nil
		}
		if err != nil {
			return out, fmt.Errorf("range read failed: %w", err)
		}
		if n == 0 {
			out.EOF = atEnd
			return out, nil
		}

		var batchFull bool
		for x := range n {
			out.BytesRead++
			curLineLength++
			if b[x] == '\n' {
				out.Offsets = append(out.Offsets, []int64{curLineStart, curLineLength})
				if len(out.Offsets) == cc.BatchSize {
					batchFull = true
					break
				}
				curLineStart = out.BytesRead
				curLineLength = 0
			}
		}

		// Consuming the whole buffer of a read that hit the end of the file is
		// the only way to know the object is exhausted. A batch that filled
		// mid-buffer has not proven that, even when atEnd is true.
		if atEnd && out.BytesRead == bufStart+int64(n) {
			out.EOF = true
			return out, nil
		}
		if batchFull {
			return out, nil
		}
	}
}

type ProcessLineInput struct {
	S3Key     string
	LineStart int64
	Length    int64
	Source    string
	Upstream  string
}

func GetLine(ctx context.Context, s3key string, start, length int64) ([]byte, error) {
	f, err := s3.GetObject(ctx, s3key)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	lineb := make([]byte, length)
	n, err := f.ReadAt(lineb, start)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("read 0 bytes, expected %d", len(lineb))
	}
	return lineb, nil
}

// ScanLines walks newline-delimited text out of r in chunkSize-byte reads,
// handing each non-blank line to fn along with the byte offset just past its
// newline.
//
// The scan covers [start, end); a negative end means "to the end of the
// object". fn returning false stops the scan there. The bool result reports
// whether the scan consumed its whole range rather than stopping early, which
// is how a planner tells "the file ran out" apart from "I have enough work".
//
// It lives here rather than in either caller because planning and reading are
// the two halves of the same decision -- one records byte ranges, the other
// re-reads them, possibly hours later and on another host -- and a difference
// of one blank line between them would mean records quietly skipped or
// processed twice. Both halves of both the scrape and periodic pipelines call
// this, so there is one answer to where a line ends and which lines count.
func ScanLines(r io.ReaderAt, start, end int64, chunkSize int, fn func(line string, lineEnd int64) bool) (bool, error) {
	if chunkSize <= 0 {
		return false, fmt.Errorf("chunk size must be positive, got %d", chunkSize)
	}

	pos := start
	var partial []byte

	for end < 0 || pos < end {
		n := chunkSize
		if end >= 0 && int64(n) > end-pos {
			n = int(end - pos)
		}

		buf := make([]byte, n)
		read, err := r.ReadAt(buf, pos)
		atEnd := errors.Is(err, io.EOF)
		if err != nil && !atEnd {
			return false, fmt.Errorf("range read at %d failed: %w", pos, err)
		}

		rest := buf[:read]
		for len(rest) > 0 {
			i := bytes.IndexByte(rest, '\n')
			if i < 0 {
				// The line runs past this read; carry it into the next one.
				partial = append(partial, rest...)
				pos += int64(len(rest))
				rest = nil
				break
			}
			line := append(partial, rest[:i]...)
			partial = nil
			pos += int64(i + 1)
			rest = rest[i+1:]
			if u := strings.TrimSpace(string(line)); u != "" {
				if !fn(u, pos) {
					return false, nil
				}
			}
		}

		// A read that came back short of the object's end with nothing in it is
		// not something ReadAt should do, but treating it as the end beats
		// spinning on it forever.
		if atEnd || read == 0 {
			break
		}
	}

	// A final line with no trailing newline is still a record.
	if u := strings.TrimSpace(string(partial)); u != "" {
		if !fn(u, pos) {
			return false, nil
		}
	}
	return true, nil
}
