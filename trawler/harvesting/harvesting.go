// harvesting contains common functions and structs for querying upstream data
// sources (like crossref) and handling the resulting piles of ndjson.
package harvesting

import (
	"context"
	"errors"
	"fmt"
	"io"

	"git.archive.org/webgroup/scholar/trawler/s3"
	"go.temporal.io/sdk/activity"
)

// TODO expose in config?
const (
	batchSize = 1000
	chunkSize = 1024 * 100
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

// FindLineBatch does a range read over an S3-stored pile of text (probably
// ndjson) and returns a list of offsets at which lines can be found. It
// reports if it hit the end of the file as well as how many bytes it read.
// This is intended to be run in parallel so a very large file can have line
// offsets extracted from different regions of the file at the same time.
func FindLineBatch(ctx context.Context, in FindLineBatchInput) (FindLineBatchOutput, error) {
	l := activity.GetLogger(ctx)
	l.Info(fmt.Sprintf("doing a range read from '%s'", in.S3Key))

	f, err := s3.GetObject(ctx, in.S3Key)
	if err != nil {
		return FindLineBatchOutput{}, err
	}
	defer f.Close()

	cc := chunkCfg{
		Offset:    in.Offset,
		BatchSize: batchSize,
		ChunkSize: chunkSize,
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

func chunk(cc chunkCfg, r io.ReaderAt) (out FindLineBatchOutput, err error) {
	out = FindLineBatchOutput{
		BytesRead: cc.Offset,
	}
	curLineStart := cc.Offset

	var done bool
	var curLineLength int64

	for !done {
		b := make([]byte, cc.ChunkSize)
		n, err := r.ReadAt(b, out.BytesRead)
		if errors.Is(err, io.EOF) {
			out.EOF = true
			err = nil
		}
		if err != nil {
			return out, fmt.Errorf("range read failed: %w", err)
		}
		if n == 0 {
			return out, nil
		}
		for x := range n {
			out.BytesRead++
			curLineLength++
			if b[x] == '\n' {
				out.Offsets = append(out.Offsets, []int64{curLineStart, curLineLength})
				if len(out.Offsets) == cc.BatchSize {
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
