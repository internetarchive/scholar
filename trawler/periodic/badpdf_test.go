package periodic

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// zeroSha1B32 is 32 base32 'A's, which decodes to twenty zero bytes -- a valid
// sha1 digest as far as decodeSha1Base32 is concerned.
var zeroSha1B32 = strings.Repeat("A", 32)

func Test_badPDFCache_key(t *testing.T) {
	c := badPDFCache{prefix: "sandcrawler/badpdf", generation: "v1"}
	require.Equal(t,
		"sandcrawler/badpdf/v1/ff/8a/ff8adff053e3aef1f43eea6bdfab3f19c990a7bc.json",
		c.key("ff8adff053e3aef1f43eea6bdfab3f19c990a7bc"))
}

// Bumping the generation has to move every lookup, because that is the whole
// wipe mechanism: the new prefix is empty, so the next run re-processes
// everything without anything having been deleted.
func Test_badPDFCache_generationMovesEveryKey(t *testing.T) {
	sha1 := "ff8adff053e3aef1f43eea6bdfab3f19c990a7bc"
	v1 := badPDFCache{prefix: "p", generation: "v1"}.key(sha1)
	v2 := badPDFCache{prefix: "p", generation: "v2"}.key(sha1)
	require.NotEqual(t, v1, v2)
	require.True(t, strings.HasPrefix(v2, "p/v2/"))
}

func Test_newBadPDFCache_defaultsAndDisable(t *testing.T) {
	viper.Reset()
	c := newBadPDFCache()
	require.True(t, c.enabled())
	require.Equal(t, defaultBadPDFPrefix, c.prefix)
	require.Equal(t, defaultBadPDFGeneration, c.generation)

	viper.Set(badPDFGenerationKey, "grobid-0.8")
	require.Equal(t, "grobid-0.8", newBadPDFCache().generation)

	// An explicit empty prefix is how you turn it off, as distinct from an
	// absent key, which just means nobody has an opinion.
	viper.Set(badPDFPrefixKey, "")
	require.False(t, newBadPDFCache().enabled())
	viper.Reset()
}

// A disabled cache must not touch S3 at all, in either direction.
func Test_badPDFCache_disabledIsInert(t *testing.T) {
	c := badPDFCache{prefix: "", generation: "v1"}
	bad, err := c.has(context.Background(), "ff8adff053e3aef1f43eea6bdfab3f19c990a7bc")
	require.NoError(t, err)
	require.False(t, bad)
	require.NoError(t, c.mark(context.Background(), "ff8adff053e3aef1f43eea6bdfab3f19c990a7bc", "r", "s"))
}

// A short digest would panic in key(). Callers always pass a 40-char digest
// from decodeSha1Base32, but a miss beats a crashed batch.
func Test_badPDFCache_shortDigestIsAMiss(t *testing.T) {
	c := badPDFCache{prefix: "p", generation: "v1"}
	bad, err := c.has(context.Background(), "ab")
	require.NoError(t, err)
	require.False(t, bad)
	require.NoError(t, c.mark(context.Background(), "ab", "r", "s"))
}

// --- ingestLine's use of it ----------------------------------------------

// fakeBadPDFs answers without S3 and records what it was asked.
type fakeBadPDFs struct {
	bad     map[string]bool
	marked  map[string]string
	hasErr  error
	markErr error
	asked   []string
}

func newFakeBadPDFs() *fakeBadPDFs {
	return &fakeBadPDFs{bad: map[string]bool{}, marked: map[string]string{}}
}

func (f *fakeBadPDFs) has(_ context.Context, sha1hex string) (bool, error) {
	f.asked = append(f.asked, sha1hex)
	if f.hasErr != nil {
		return false, f.hasErr
	}
	return f.bad[sha1hex], nil
}

func (f *fakeBadPDFs) mark(_ context.Context, sha1hex, reason, _ string) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.marked[sha1hex] = reason
	return nil
}

// cacheDeps is a lineDeps with no HTTP client and no PDF processor, so any test
// using it proves the code path never reached the network: touching either
// would panic on a nil pointer.
func cacheDeps(idx badPDFIndex) lineDeps {
	return lineDeps{
		Source:    "test",
		ItemID:    "ITEM-1",
		SkipRules: func() (pdfSkipRules, error) { return pdfSkipRules{}, nil },
		BadPDFs:   idx,
		Beat:      func(string) {},
		Log:       slog.Default(),
	}
}

func cdxRow(sha1b32 string) string {
	return `{"Sha1Base32":"` + sha1b32 + `","URL":"http://example.org/a.pdf",` +
		`"Timestamp":"20250101000000","WarcPath":"ITEM-1/x.warc.gz"}`
}

func hexOf(t *testing.T, b32 string) string {
	t.Helper()
	bs, err := base32.StdEncoding.DecodeString(b32)
	require.NoError(t, err)
	return hex.EncodeToString(bs)
}

// The point of the cache is that a hit costs nothing but the lookup. If the row
// got as far as the elasticsearch checks this would panic on the nil client,
// which is exactly the assertion.
func Test_ingestLine_cacheHitSkipsBeforeAnyNetworkWork(t *testing.T) {
	f := newFakeBadPDFs()
	f.bad[hexOf(t, zeroSha1B32)] = true

	counts, oc, err := ingestLine(context.Background(), cacheDeps(f), cdxRow(zeroSha1B32))
	require.NoError(t, err)

	require.Equal(t, 1, counts.Lines)
	require.Equal(t, 1, counts.PdfsKnownBad)
	// Counted apart from the ordinary skips, so a replay's log shows what the
	// cache saved rather than burying it in the usual noise.
	require.Equal(t, 0, counts.PdfsSkipped)
	require.Equal(t, StatusSkip, oc.Status)
	require.Contains(t, oc.Detail, "known-bad")
	require.Equal(t, zeroSha1B32, oc.Sha1B32)
	require.Equal(t, []string{hexOf(t, zeroSha1B32)}, f.asked)
}

// A cache that cannot answer must not stop the row: an unreachable or wiped
// cache changes how long a run takes, never what it concludes. Here that means
// carrying on into the lookups, which with a nil client panics -- so the test
// asserts the panic rather than pretending the network is available.
func Test_ingestLine_cacheErrorFallsThroughToProcessing(t *testing.T) {
	f := newFakeBadPDFs()
	f.hasErr = errors.New("seaweed unreachable")

	require.Panics(t, func() {
		ingestLine(context.Background(), cacheDeps(f), cdxRow(zeroSha1B32))
	}, "a cache error should have fallen through to real processing")
}

// The denylists are consulted before the cache, since they need no round trip
// at all.
func Test_ingestLine_denylistBeatsTheCache(t *testing.T) {
	f := newFakeBadPDFs()
	d := cacheDeps(f)
	d.SkipRules = func() (pdfSkipRules, error) {
		return pdfSkipRules{CursedShas: []string{zeroSha1B32}}, nil
	}

	counts, oc, err := ingestLine(context.Background(), d, cdxRow(zeroSha1B32))
	require.NoError(t, err)
	require.Equal(t, 1, counts.PdfsSkipped)
	require.Equal(t, 0, counts.PdfsKnownBad)
	require.Equal(t, "cursed sha1", oc.Detail)
	require.Empty(t, f.asked, "the cache was consulted for a denylisted sha1")
}

// A row that will not even parse never reaches the cache, so corruption in a
// staged list cannot write markers for whatever it happens to decode to.
func Test_ingestLine_unparsableRowNeverTouchesTheCache(t *testing.T) {
	f := newFakeBadPDFs()

	counts, oc, err := ingestLine(context.Background(), cacheDeps(f), "{not json")
	require.NoError(t, err)
	require.Equal(t, 1, counts.PdfsSkipped)
	require.Equal(t, StatusSkip, oc.Status)
	require.Empty(t, f.asked)
	require.Empty(t, f.marked)
}
