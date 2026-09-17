package periodic

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git.archive.org/webgroup/scholar/trawler/s3"
	"github.com/spf13/viper"
)

// The negative cache: sha1s we have already put through petabox and GROBID and
// got nothing usable out of.
//
// It exists because nothing else remembers them. A row that ends in a fatcat
// file record is recognised on the next pass by the lookups at the top of
// ingestLine, and costs four HTTP round trips to skip. A row that ends in "this
// PDF is no good" leaves no trace anywhere, so every later pass over the same
// collection downloads the PDF again and waits out GROBID again to reach the
// same conclusion. On a wide crawl that is most of the rows and nearly all of
// the time.
//
// What goes in it is deliberately narrow: verdicts we reached by processing the
// PDF, where the verdict is a property of the bytes and of how we process them.
// Two things are kept out.
//
//   - Failures. A lookup that errored, a range read that 503'd, a GROBID that
//     timed out: those are facts about the last few seconds, not about the PDF.
//     Caching them would let one bad afternoon durably blacklist a slice of the
//     collection. The cache is written from verdict paths only, which is what
//     makes that structurally impossible rather than merely unlikely.
//   - "No release in fatcat matches this PDF's external IDs." That answer
//     changes on its own as fatcat grows, with nothing about the PDF or our
//     processing having changed, so a cached "no" would quietly strand a paper
//     the day its release landed.
//
// It is distinct from periodic.cursed_shas, which is a hand-maintained denylist
// of PDFs that wedge the pipeline. This is machine-written and safe to throw
// away; that is human-written and is not.
//
// Every operation is advisory. A read that fails is a miss and the PDF gets
// processed; a write that fails is logged and the row still counts. The cache
// can be wiped, corrupted or unreachable without changing what a run concludes
// about any PDF -- only how long it takes to conclude it.

const (
	badPDFPrefixKey     = "periodic.bad_pdf_prefix"
	badPDFGenerationKey = "periodic.bad_pdf_generation"

	defaultBadPDFPrefix = "sandcrawler/badpdf"
	// defaultBadPDFGeneration is the generation everything lands under until
	// somebody says otherwise. See badPDFCache.
	defaultBadPDFGeneration = "v1"
)

// badPDFIndex is the cache as ingestLine uses it, so a test can substitute one
// that answers without S3.
type badPDFIndex interface {
	// has reports whether this sha1 has already been found unusable. An error
	// means "could not tell", and callers treat that as a miss.
	has(ctx context.Context, sha1hex string) (bool, error)
	// mark records a sha1 as unusable, with a human-readable reason.
	mark(ctx context.Context, sha1hex, reason, source string) error
}

// badPDFCache stores one small object per known-bad sha1.
//
// The generation sits in the key path, which is what makes the cache wipeable
// without deleting anything: changing periodic.bad_pdf_generation moves every
// lookup to a prefix that is empty, so the next run re-processes everything and
// the old entries simply stop being consulted. That is the right shape for
// "I changed how PDFs are processed" -- it takes effect the moment the workers
// restart onto the new config, it is reversible if the change is rolled back,
// and reclaiming the space is a recursive delete you can do whenever, or never.
type badPDFCache struct {
	prefix     string
	generation string
}

// newBadPDFCache reads the cache's configuration. An empty prefix disables it.
func newBadPDFCache() badPDFCache {
	prefix := defaultBadPDFPrefix
	if viper.IsSet(badPDFPrefixKey) {
		// An explicit empty prefix means "off", which is worth being able to
		// say; an absent key means we have no opinion and take the default.
		prefix = viper.GetString(badPDFPrefixKey)
	}

	generation := viper.GetString(badPDFGenerationKey)
	if generation == "" {
		generation = defaultBadPDFGeneration
	}

	return badPDFCache{prefix: prefix, generation: generation}
}

func (c badPDFCache) enabled() bool { return c.prefix != "" }

// key is where a sha1's marker lives. The two-byte fan-out matches seaweedKey,
// so a generation's worth of markers browses the same way the PDF blobs do.
func (c badPDFCache) key(sha1hex string) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s.json",
		c.prefix, c.generation, sha1hex[0:2], sha1hex[2:4], sha1hex)
}

// badPDFRecord is the marker's content. The lookup is a HEAD and never reads
// it; it is here so that somebody who finds a PDF being skipped can ask why
// without correlating timestamps against a worker's outcome log.
type badPDFRecord struct {
	Sha1   string    `json:"sha1"`
	Reason string    `json:"reason"`
	Source string    `json:"source"`
	At     time.Time `json:"at"`
}

func (c badPDFCache) has(ctx context.Context, sha1hex string) (bool, error) {
	if !c.usable(sha1hex) {
		return false, nil
	}
	return s3.ObjectExists(ctx, c.key(sha1hex))
}

func (c badPDFCache) mark(ctx context.Context, sha1hex, reason, source string) error {
	if !c.usable(sha1hex) {
		return nil
	}

	body, err := json.Marshal(badPDFRecord{
		Sha1:   sha1hex,
		Reason: reason,
		Source: source,
		At:     time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("could not encode bad-pdf record for %s: %w", sha1hex, err)
	}

	return s3.PutObject(ctx, c.key(sha1hex), body, "application/json")
}

// usable guards the key arithmetic. Callers reach this with a digest from
// decodeSha1Base32, which is always 40 hex characters, but a short one would
// panic in key() rather than merely miss.
func (c badPDFCache) usable(sha1hex string) bool {
	return c.enabled() && len(sha1hex) >= 4
}
