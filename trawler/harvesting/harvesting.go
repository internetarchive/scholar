// harvesting contains common functions and structs for querying upstream data
// sources (like crossref) and handling the resulting piles of ndjson. It wraps
// scholkit as an external process.
package harvesting

import (
	"context"
	"errors"
	"fmt"
	"io"

	"git.archive.org/webgroup/scholar/trawler/s3"
	"go.temporal.io/sdk/activity"
)

type FindLineBatchInput struct {
	S3Key  string
	Offset int64
}

type FindLineBatchOutput struct {
	Offsets   [][]int64
	BytesRead int64
	EOF       bool
}

func FindLineBatch(ctx context.Context, in FindLineBatchInput) (out FindLineBatchOutput, err error) {
	out = FindLineBatchOutput{}

	l := activity.GetLogger(ctx)
	l.Info(fmt.Sprintf("doing a range read from '%s'", in.S3Key))

	f, err := s3.GetObject(ctx, in.S3Key)
	if err != nil {
		return
	}
	defer f.Close()

	batchSize := 1000 // TODO set in config

	// TODO refactor this so it's unit testable
	chunkSize := 1024 * 100 // TODO set in config
	out.BytesRead = in.Offset
	curLineStart := in.Offset

	var done bool
	var curLineLength int64

	for !done {
		b := make([]byte, chunkSize)
		n, err := f.ReadAt(b, out.BytesRead)
		l.Debug(fmt.Sprintf("read %d bytes", n))
		if errors.Is(err, io.EOF) {
			l.Debug("saw EOF")
			out.EOF = true
			err = nil
		}
		if err != nil {
			return out, fmt.Errorf("range read of '%s' failed: %w", in.S3Key, err)
		}
		if n == 0 {
			return out, nil
		}
		for x := range n {
			out.BytesRead++
			curLineLength++
			if b[x] == '\n' {
				out.Offsets = append(out.Offsets, []int64{curLineStart, curLineLength})
				if len(out.Offsets) == batchSize {
					done = true
					break
				}
				curLineStart = out.BytesRead
				curLineLength = 0
			}
		}

		if out.EOF {
			done = true
		}
	}

	return
}
