package scrape

import (
	"context"
	"fmt"
	"io"

	"git.archive.org/webgroup/scholar/trawler/harvesting"
	"git.archive.org/webgroup/scholar/trawler/s3"
)

// Planning is the half of a scrape that decides what a unit of work is. It
// walks the URL list and cuts it into byte ranges; a range is what a crawl
// activity is handed.
//
// The URLs themselves never reach the workflow. That is the whole point of the
// split: a run's Temporal history is then a function of how many batches it
// dispatched rather than how many URLs it crawled, and a batch is two int64s
// and an int rather than a few hundred URL strings.

// BatchRef names a contiguous slice of the URL list: one crawl activity's
// worth of work.
//
// Offset and End are byte positions in the list object, End being just past
// the final URL's trailing newline. So [Offset, End) re-reads exactly this
// batch's lines however many times it is retried, and End is where the next
// batch begins. URLCount is how many non-blank lines the range holds, which is
// what the workflow counts against urls_per_can.
type BatchRef struct {
	Offset   int64
	End      int64
	URLCount int
}

// URLRef is one URL from the list plus the byte offset just past its trailing
// newline, which is where a resume picks up.
type URLRef struct {
	URL string
	End int64
}

type PlanBatchesInput struct {
	S3Key string
	// Offset is the byte position in the list to start cutting from.
	Offset int64
	// BatchSize is how many URLs go in one batch.
	BatchSize int
	// MaxBatches bounds how many batches one call plans. It exists so a single
	// planning activity reads a bounded slice of the list rather than all of
	// it: with the defaults this is a megabyte or so of URLs per call.
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
	// last batches in the list.
	EOF bool
}

// PlanBatchesActivity cuts the next stretch of the URL list into batches.
func PlanBatchesActivity(ctx context.Context, in PlanBatchesInput) (PlanBatchesOutput, error) {
	if in.BatchSize <= 0 || in.MaxBatches <= 0 || in.ChunkSize <= 0 {
		return PlanBatchesOutput{NextOffset: in.Offset},
			fmt.Errorf("scrape: bad plan config (batch=%d max=%d chunk=%d)",
				in.BatchSize, in.MaxBatches, in.ChunkSize)
	}

	obj, err := s3.GetObject(ctx, in.S3Key)
	if err != nil {
		return PlanBatchesOutput{NextOffset: in.Offset}, err
	}
	defer obj.Close()

	out, err := planBatches(in, obj)
	if err != nil {
		return out, fmt.Errorf("scrape: planning %s: %w", in.S3Key, err)
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
	// resume from landing in the middle of a batch somebody is already crawling.
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
			Offset:   batchStart,
			End:      end,
			URLCount: count,
		})
		batchStart = end
		count = 0
		return len(out.Batches) < in.MaxBatches
	})
	if err != nil {
		return PlanBatchesOutput{NextOffset: in.Offset}, err
	}

	if consumed {
		// The list ran out mid-batch. A short final batch is still a batch --
		// dropping it would silently abandon the tail of every list whose
		// length is not a multiple of BatchSize.
		if count > 0 {
			out.Batches = append(out.Batches, BatchRef{
				Offset:   batchStart,
				End:      lastEnd,
				URLCount: count,
			})
			batchStart = lastEnd
		}
		out.EOF = true
	}

	out.NextOffset = batchStart
	return out, nil
}

// readBatchURLs pulls one batch's URLs out of the list. This is the read the
// old per-URL design paid once per batch and then put in workflow history;
// here it stays inside the activity that is about to crawl them.
func readBatchURLs(ctx context.Context, s3key string, ref BatchRef, chunkSize int) ([]URLRef, error) {
	obj, err := s3.GetObject(ctx, s3key)
	if err != nil {
		return nil, err
	}
	defer obj.Close()

	urls, err := sliceURLs(ref, chunkSize, obj)
	if err != nil {
		return nil, fmt.Errorf("scrape: reading %s: %w", s3key, err)
	}
	return urls, nil
}

// sliceURLs is the reading logic, split out from readBatchURLs so it can be
// tested without S3.
func sliceURLs(ref BatchRef, chunkSize int, r io.ReaderAt) ([]URLRef, error) {
	out := make([]URLRef, 0, ref.URLCount)
	if _, err := scanLines(r, ref.Offset, ref.End, chunkSize, func(u string, end int64) bool {
		out = append(out, URLRef{URL: u, End: end})
		return true
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// scanLines is harvesting.ScanLines under the name the rest of this package
// uses. The scan itself is shared with the periodic pipeline, which walks its
// PDF-CDX lists exactly the same way; see that function for why planning and
// reading must not be able to disagree about where a line ends.
func scanLines(r io.ReaderAt, start, end int64, chunkSize int, fn func(line string, lineEnd int64) bool) (bool, error) {
	return harvesting.ScanLines(r, start, end, chunkSize, fn)
}
