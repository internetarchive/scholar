package etd

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testFetcher(maxBytes int64) *waybackFetcher {
	return &waybackFetcher{client: http.DefaultClient, retries: 3, backoff: time.Millisecond, stall: 5 * time.Second, maxBytes: maxBytes}
}

func TestWaybackFetcher(t *testing.T) {
	pdfBody := []byte("junk%PDF-1.7\nhello")
	var flaky atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pdf":
			assert.Contains(t, r.Header.Get("User-Agent"), "archive.org_bot")
			w.Write(pdfBody)
		case "/redirect":
			http.Redirect(w, r, "/pdf", http.StatusFound)
		case "/html":
			w.Write([]byte("<html>not archived</html>"))
		case "/flaky":
			if flaky.Add(1) < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Write(pdfBody)
		case "/down":
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	f := testFetcher(1 << 20)

	sum := sha1.Sum(pdfBody)
	c, err := f.Checksums(ctx, srv.URL+"/redirect")
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(sum[:]), c.Sha1)
	assert.Equal(t, len(pdfBody), c.Size)
	assert.Len(t, c.Sha256, 64)
	assert.Len(t, c.Md5, 32)
	assert.Equal(t, srv.URL+"/pdf", c.WaybackURL, "the URL the redirects landed on")

	_, err = f.Checksums(ctx, srv.URL+"/html")
	assert.True(t, isPermanent(err), "not a pdf is a verdict: %v", err)
	_, err = f.Checksums(ctx, srv.URL+"/gone")
	assert.True(t, isPermanent(err), "a 404 is a verdict: %v", err)

	c, err = f.Checksums(ctx, srv.URL+"/flaky")
	require.NoError(t, err, "503s are retried")
	assert.Equal(t, hex.EncodeToString(sum[:]), c.Sha1)

	_, err = f.Checksums(ctx, srv.URL+"/down")
	require.Error(t, err)
	assert.False(t, isPermanent(err), "a 502 that never clears is worth retrying later")

	_, err = testFetcher(5).Checksums(ctx, srv.URL+"/pdf")
	assert.True(t, isPermanent(err), "oversized: %v", err)

	bs, err := f.Bytes(ctx, srv.URL+"/pdf")
	require.NoError(t, err)
	assert.Equal(t, pdfBody, bs)
}
