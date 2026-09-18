package daily

import (
	"context"
	"fmt"
	"io"

	"git.archive.org/webgroup/scholar/trawler/harvesting"
	"git.archive.org/webgroup/scholar/trawler/s3"
)

// Planning is the half of a day that decides what a unit of work is. It walks
// the scraped .ndjson and cuts it into byte ranges; a range is what a crawl
// activity is handed.
//
// The lines themselves never reach the workflow. That is the whole point of
// the split: a run's Temporal history is then a function of how many batches
// it dispatched rather than how many records it worked, and a batch is two
// int64s and an int rather than a few hundred crossref documents -- which are
// kilobytes each, and which the old per-line design put in workflow history
// by reference on every single line.

// BatchRef names a contiguous slice of the .ndjson: one crawl activity's worth
// of work.
//
// Offset and End are byte positions in the object, End being just past the
// final line's trailing newline. So [Offset, End) re-reads exactly this
// batch's lines however many times it is retried, and End is where the next
// batch begins. LineCount is how many non-blank lines the range holds, which
// is what the workflow counts against lines_per_can.
type BatchRef struct {
	Offset    int64
	End       int64
	LineCount int
}

// LineRef is one ndjson line plus the byte offset just past its trailing
// newline, which is where a resume picks up.
type LineRef struct {
	JSON string
	End  int64
}

type PlanBatchesInput struct {
	S3Key string
	// Offset is the byte position in the object to start cutting from.
	Offset int64
	// BatchSize is how many lines go in one batch.
	BatchSize int
	// MaxBatches bounds how many batches one call plans. It exists so a single
	// planning activity reads a bounded slice of the object rather than all of
	// it, which for a day of crossref is gigabytes.
	MaxBatches int
	// ChunkSize is how many bytes one range read pulls.
	ChunkSize int
}

type PlanBatchesOutput struct {
	Batches []BatchRef
	// NextOffset is where the next planning call resumes: the end of the last
	// batch planned, never mid-batch.
	NextOffset int64
	// EOF reports that the scan reached the end of the object, so these are the
	// last batches of the day.
	EOF bool
}

// PlanBatchesActivity cuts the next stretch of the scraped .ndjson into
// batches.
func PlanBatchesActivity(ctx context.Context, in PlanBatchesInput) (PlanBatchesOutput, error) {
	if in.BatchSize <= 0 || in.MaxBatches <= 0 || in.ChunkSize <= 0 {
		return PlanBatchesOutput{NextOffset: in.Offset},
			fmt.Errorf("daily: bad plan config (batch=%d max=%d chunk=%d)",
				in.BatchSize, in.MaxBatches, in.ChunkSize)
	}

	obj, err := s3.GetObject(ctx, in.S3Key)
	if err != nil {
		return PlanBatchesOutput{NextOffset: in.Offset}, err
	}
	defer obj.Close()

	out, err := planBatches(in, obj)
	if err != nil {
		return out, fmt.Errorf("daily: planning %s: %w", in.S3Key, err)
	}
	return out, nil
}

// planBatches is the cutting logic, split out from the activity so it can be
// tested without S3.
func planBatches(in PlanBatchesInput, r io.ReaderAt) (PlanBatchesOutput, error) {
	out := PlanBatchesOutput{NextOffset: in.Offset}

	// batchStart trails one batch behind the scan: it is the end of the last
	// batch emitted, which is both where the next batch begins and the offset
	// the caller resumes from. Cutting only on a batch boundary is what keeps a
	// resume from landing in the middle of a batch somebody is already working.
	batchStart := in.Offset
	count := 0
	lastEnd := in.Offset

	consumed, err := scanLines(r, in.Offset, -1, in.ChunkSize, func(_ string, end int64) bool {
		count++
		lastEnd = end
		if count < in.BatchSize {
			return true
		}
		out.Batches = append(out.Batches, BatchRef{
			Offset:    batchStart,
			End:       end,
			LineCount: count,
		})
		batchStart = end
		count = 0
		return len(out.Batches) < in.MaxBatches
	})
	if err != nil {
		return PlanBatchesOutput{NextOffset: in.Offset}, err
	}

	if consumed {
		// The object ran out mid-batch. A short final batch is still a batch --
		// dropping it would silently abandon the tail of every day whose record
		// count is not a multiple of BatchSize.
		if count > 0 {
			out.Batches = append(out.Batches, BatchRef{
				Offset:    batchStart,
				End:       lastEnd,
				LineCount: count,
			})
			batchStart = lastEnd
		}
		out.EOF = true
	}

	out.NextOffset = batchStart
	return out, nil
}

// readBatchLines pulls one batch's records out of the .ndjson. This is the
// read the old per-line design paid once per line and then put in workflow
// history; here it stays inside the activity that is about to work them.
func readBatchLines(ctx context.Context, s3key string, ref BatchRef, chunkSize int) ([]LineRef, error) {
	obj, err := s3.GetObject(ctx, s3key)
	if err != nil {
		return nil, err
	}
	defer obj.Close()

	lines, err := sliceLines(ref, chunkSize, obj)
	if err != nil {
		return nil, fmt.Errorf("daily: reading %s: %w", s3key, err)
	}
	return lines, nil
}

// sliceLines is the reading logic, split out from readBatchLines so it can be
// tested without S3.
func sliceLines(ref BatchRef, chunkSize int, r io.ReaderAt) ([]LineRef, error) {
	out := make([]LineRef, 0, ref.LineCount)
	if _, err := scanLines(r, ref.Offset, ref.End, chunkSize, func(line string, end int64) bool {
		out = append(out, LineRef{JSON: line, End: end})
		return true
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// scanLines is harvesting.ScanLines under the name the rest of this package
// uses. The scan itself is shared with the scrape and periodic pipelines,
// which walk their lists exactly the same way; see that function for why
// planning and reading must not be able to disagree about where a line ends.
func scanLines(r io.ReaderAt, start, end int64, chunkSize int, fn func(line string, lineEnd int64) bool) (bool, error) {
	return harvesting.ScanLines(r, start, end, chunkSize, fn)
}
