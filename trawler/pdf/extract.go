package pdf

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image/jpeg"

	"github.com/gen2brain/go-fitz"
)

// This file is the entire cgo surface of the PDF pipeline: two calls into
// MuPDF, one for text and one for the page-0 thumbnail.
//
// Keeping them here and keeping them to two is deliberate. A malformed PDF can
// segfault inside MuPDF, which no amount of Go error handling can catch -- the
// process simply dies -- so the only real defence is to run these in a child
// process and let it be the thing that dies. That is much easier to do against
// two functions in one file than against a dependency's internals, and it is
// why the inlined version drops the metadata pass blobproc also ran: nothing in
// trawler consumed it, and it was a third way into the same crash.
//
// Anything added to this file should be assumed capable of taking the process
// down with it.

const (
	// thumbWidth and thumbHeight bound the page-0 thumbnail in pixels. These are
	// the numbers scholar's web UI has always been served, so they are part of
	// the derivative contract rather than a tunable.
	thumbWidth  = 180
	thumbHeight = 300

	// thumbJPEGQuality matches what blobproc encoded at.
	thumbJPEGQuality = 90

	// minThumbnailSize is the size below which we assume rendering went wrong
	// and treat the result as no thumbnail at all. Inherited from blobproc,
	// whose comment read: "assuming that very small images mean something went
	// wrong".
	minThumbnailSize = 50
)

// pdfMagic is what every PDF starts with. Some have leading junk before it, so
// callers look for it within a prefix rather than at byte zero.
var pdfMagic = []byte("%PDF-")

// magicSearchWindow is how far into the file we look for pdfMagic.
const magicSearchWindow = 1024

// looksLikePDF is the cheap prefilter in front of MuPDF.
//
// blobproc used mimetype.Detect for this, which sniffs the same magic bytes and
// cost a dependency plus a scan of the blob. Doing it directly says what we
// actually require and is honest that it is a smell test, not validation.
func looksLikePDF(blob []byte) bool {
	window := blob
	if len(window) > magicSearchWindow {
		window = window[:magicSearchWindow]
	}
	return bytes.Contains(window, pdfMagic)
}

// sha1Hex is the content address a PDF's derivatives are keyed by.
func sha1Hex(blob []byte) string {
	sum := sha1.Sum(blob)
	return hex.EncodeToString(sum[:])
}

// extractText returns the full text of the PDF via MuPDF.
//
// Pages are separated by form-feed (\f) to mirror pdftotext's default page
// separator. Text comes back in MuPDF reading order; there is no column-aware
// whitespace preservation analogous to "pdftotext -layout".
func extractText(ctx context.Context, blob []byte) ([]byte, error) {
	doc, err := fitz.NewFromMemory(blob)
	if err != nil {
		return nil, fmt.Errorf("fitz open: %w", err)
	}
	defer doc.Close()

	var buf bytes.Buffer
	n := doc.NumPage()
	for i := range n {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		txt, err := doc.Text(i)
		if err != nil {
			return nil, fmt.Errorf("fitz text page %d: %w", i, err)
		}
		buf.WriteString(txt)
		if i < n-1 {
			buf.WriteByte('\f')
		}
	}
	return buf.Bytes(), nil
}

// extractThumbnail renders page 0 via MuPDF and encodes it as JPEG.
//
// The rendering DPI is computed from the page bounds so the image fits within
// thumbWidth x thumbHeight. This is the call that segfaulted on a malformed PDF
// in production; see the note at the top of this file.
func extractThumbnail(ctx context.Context, blob []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	doc, err := fitz.NewFromMemory(blob)
	if err != nil {
		return nil, fmt.Errorf("fitz open: %w", err)
	}
	defer doc.Close()

	if doc.NumPage() == 0 {
		return nil, fmt.Errorf("pdf has no pages")
	}

	bounds, err := doc.Bound(0)
	if err != nil {
		return nil, fmt.Errorf("fitz page bounds: %w", err)
	}

	img, err := doc.ImageDPI(0, thumbnailDPI(bounds.Dx(), bounds.Dy()))
	if err != nil {
		return nil, fmt.Errorf("fitz render page 0: %w", err)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: thumbJPEGQuality}); err != nil {
		return nil, fmt.Errorf("jpeg encode: %w", err)
	}
	return buf.Bytes(), nil
}

// thumbnailDPI is the render resolution that fits a page of the given size, in
// points, inside the thumbnail box. The smaller of the two axis ratios wins so
// neither dimension overflows; a page reporting no size at all falls back to
// something sane rather than dividing by zero.
func thumbnailDPI(widthPts, heightPts int) float64 {
	const (
		pointsPerInch = 72.0
		fallbackDPI   = 150.0
	)
	if widthPts <= 0 || heightPts <= 0 {
		return fallbackDPI
	}
	dpiX := float64(thumbWidth) * pointsPerInch / float64(widthPts)
	dpiY := float64(thumbHeight) * pointsPerInch / float64(heightPts)
	return min(dpiX, dpiY)
}
