// harvesting holds what the three pipelines that walk a pile of
// newline-delimited text in S3 -- daily, scrape and periodic -- have to agree
// about: where a line ends and which lines count.
//
// It is deliberately down to one function. Each pipeline plans its own work
// into byte ranges and reads those ranges back inside the activity that does
// the work, but planning and reading are two halves of one decision, made
// hours apart and often on different hosts, so they both go through ScanLines.
package harvesting

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

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
