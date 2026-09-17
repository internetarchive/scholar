package pdf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testSHA1 = "ff8adff053e3aef1f43eea6bdfab3f19c990a7bc"

// --- derivative keys ------------------------------------------------------

// These keys are read by systems that know nothing about trawler -- scholar's
// web UI serves the thumbnails, the reindex path reads the TEI -- and they were
// written this way for years by blobproc. Inlining that code must not have
// moved a single object, so the exact strings are pinned here rather than
// derived, and a change to this test is a migration.
func Test_derivativePath(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    derivative
		want string
	}{
		{"text", textDerivative, "text/ff/8a/ff8adff053e3aef1f43eea6bdfab3f19c990a7bc.txt"},
		{"thumbnail", thumbnailDerivative, "pdf/ff/8a/ff8adff053e3aef1f43eea6bdfab3f19c990a7bc.180px.jpg"},
		{"tei", teiDerivative, "grobid/ff/8a/ff8adff053e3aef1f43eea6bdfab3f19c990a7bc.tei.xml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, derivativePath(tc.d.folder, testSHA1, tc.d.ext))
		})
	}

	require.Equal(t, "sandcrawler", textDerivative.bucket)
	require.Equal(t, "thumbnail", thumbnailDerivative.bucket)
	require.Equal(t, "sandcrawler", teiDerivative.bucket)
}

// The extension encodes the thumbnail width, so the two cannot drift apart
// without the stored objects becoming a lie.
func Test_thumbnailDerivativeMatchesThumbWidth(t *testing.T) {
	require.Equal(t, fmt.Sprintf("%dpx.jpg", thumbWidth), thumbnailDerivative.ext)
}

func Test_derivativePath_addsMissingDot(t *testing.T) {
	require.Equal(t,
		"grobid/ff/8a/ff8adff053e3aef1f43eea6bdfab3f19c990a7bc.tei.xml",
		derivativePath("grobid", testSHA1, ".tei.xml"))
}

// --- thumbnail geometry ---------------------------------------------------

// Reproduces blobproc's arithmetic: the smaller axis ratio wins so neither
// dimension overflows the box.
func Test_thumbnailDPI(t *testing.T) {
	// US Letter at 612x792 points. 180*72/612 = 21.18, 300*72/792 = 27.27.
	require.InDelta(t, 21.176, thumbnailDPI(612, 792), 0.001)
	// A square page is bounded by the 180px width, not the 300px height.
	require.InDelta(t, 12.96, thumbnailDPI(1000, 1000), 0.001)
	// A tall narrow page is the case where the height binds instead.
	require.InDelta(t, 10.8, thumbnailDPI(200, 2000), 0.001)

	// blobproc divided by these without checking, which is +Inf and then a
	// pixmap request nobody wants.
	require.Equal(t, 150.0, thumbnailDPI(0, 792))
	require.Equal(t, 150.0, thumbnailDPI(612, 0))
	require.Equal(t, 150.0, thumbnailDPI(-1, -1))
}

// --- prefilter ------------------------------------------------------------

func Test_looksLikePDF(t *testing.T) {
	require.True(t, looksLikePDF([]byte("%PDF-1.7\nbody")))
	// Some PDFs carry junk before the header; blobproc's mimetype sniffing
	// tolerated that and so must this.
	require.True(t, looksLikePDF(append(make([]byte, 200), []byte("%PDF-1.4")...)))

	require.False(t, looksLikePDF([]byte("<html><body>not a pdf</body></html>")))
	require.False(t, looksLikePDF(nil))
	// Beyond the search window it does not count, so a huge non-PDF with the
	// string buried in it is still rejected.
	require.False(t, looksLikePDF(append(make([]byte, magicSearchWindow+10), []byte("%PDF-1.4")...)))
}

func Test_sha1Hex(t *testing.T) {
	require.Equal(t, "da39a3ee5e6b4b0d3255bfef95601890afd80709", sha1Hex(nil))
	require.Len(t, sha1Hex([]byte("x")), expectedSHA1Length)
}

// --- the known-bad list ---------------------------------------------------

// The list came across from blobproc with one duplicate removed. If it ever
// empties out by accident, PDFs known to wedge the library would start reaching
// it again.
func Test_knownBadSHA1(t *testing.T) {
	require.Len(t, knownBadSHA1, 151)
	require.True(t, knownBadSHA1["011478a1e63a2a31eae1a93832a74cc95f220760"])
	require.False(t, knownBadSHA1[testSHA1])

	for sha := range knownBadSHA1 {
		require.Len(t, sha, expectedSHA1Length, "entry is not a hex sha1: %q", sha)
		require.Equal(t, strings.ToLower(sha), sha, "entry is not lowercase: %q", sha)
	}
}

// --- GROBID classification -----------------------------------------------

func Test_grobidRejection(t *testing.T) {
	code, ok := grobidRejection([]byte("[NO_BLOCKS] no blocks found"))
	require.True(t, ok)
	require.Equal(t, "[NO_BLOCKS]", code)

	_, ok = grobidRejection([]byte("[BAD_INPUT_DATA]"))
	require.True(t, ok)

	// Anything we do not recognise must not be read as "the PDF is bad" -- it
	// could be GROBID having an outage, and writing off good PDFs in bulk is
	// the expensive mistake.
	_, ok = grobidRejection([]byte("502 Bad Gateway"))
	require.False(t, ok)
	_, ok = grobidRejection(nil)
	require.False(t, ok)
}

func Test_isTimeout(t *testing.T) {
	// The shape net/http's client timeout actually arrives in.
	require.True(t, isTimeout(&url.Error{
		Op:  "Post",
		URL: "http://grobid/api",
		Err: errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)"),
	}))
	require.True(t, isTimeout(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)))
	require.True(t, isTimeout(&net.DNSError{IsTimeout: true}))

	// A refused connection is GROBID being down, not slow. It must stay a hard
	// failure so the run stops instead of blaming a thousand PDFs.
	require.False(t, isTimeout(errors.New("connection refused")))
	require.False(t, isTimeout(&net.DNSError{}))
}

// --- CacheableVerdict ----------------------------------------------------

// The whole point of this function: which absences of TEI are facts about the
// PDF, and so safe for a caller to remember forever.
func Test_CacheableVerdict(t *testing.T) {
	for _, tc := range []struct {
		name     string
		content  Content
		wantOK   bool
		contains string
	}{
		{
			name:     "grobid rejected the pdf",
			content:  Content{GrobidOutcome: GrobidRejected, GrobidRejection: "[NO_BLOCKS]"},
			wantOK:   true,
			contains: "[NO_BLOCKS]",
		},
		{
			name:     "200 with an empty body",
			content:  Content{GrobidOutcome: GrobidOK},
			wantOK:   true,
			contains: "no xml",
		},
		{
			// Caching this would blacklist good PDFs every time GROBID gets busy.
			name:    "timeout is not the pdf's fault",
			content: Content{GrobidOutcome: GrobidTimeout},
			wantOK:  false,
		},
		{
			// Caching this would strand the PDF the day somebody raised the limit.
			name:    "size skip is our config, not the pdf",
			content: Content{GrobidOutcome: GrobidTooLarge},
			wantOK:  false,
		},
		{
			name:    "there is TEI, so there is no verdict to cache",
			content: Content{GrobidXML: []byte("<TEI/>")},
			wantOK:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := tc.content.CacheableVerdict()
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				require.Contains(t, reason, tc.contains)
			} else {
				require.Empty(t, reason)
			}
		})
	}
}
