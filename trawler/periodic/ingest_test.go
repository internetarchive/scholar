package periodic

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/miku/grobidclient/tei"
	"github.com/spf13/viper"
)

func Test_toWaybackURL(t *testing.T) {
	viper.Set("wayback.replay_endpoint", "https://web.archive.org/web/")

	got := toWaybackURL("20250825174138", "http://verlag.nhm-wien.ac.at/pdfs/113A_373510_Janssen.pdf")
	want := "https://web.archive.org/web/20250825174138id_/http://verlag.nhm-wien.ac.at/pdfs/113A_373510_Janssen.pdf"
	if got != want {
		t.Errorf("scheme not preserved:\n got %q\nwant %q", got, want)
	}

	// Query string must survive intact.
	got = toWaybackURL("20250101000000", "https://ex.com/a?id=5&x=1")
	want = "https://web.archive.org/web/20250101000000id_/https://ex.com/a?id=5&x=1"
	if got != want {
		t.Errorf("query not preserved:\n got %q\nwant %q", got, want)
	}
}

// gzipWARCResponse builds a single gzipped WARC "response" record wrapping an
// HTTP/1.1 200 response with the given body. If truncated is non-empty a
// WARC-Truncated header is added. httpContentLen is what the HTTP
// Content-Length header advertises (set it > len(body) to model a capture cut
// short mid-download).
func gzipWARCResponse(t *testing.T, truncated string, httpContentLen int, body []byte) []byte {
	t.Helper()

	var httpMsg bytes.Buffer
	fmt.Fprintf(&httpMsg, "HTTP/1.1 200 OK\r\nContent-Type: application/pdf\r\nContent-Length: %d\r\n\r\n", httpContentLen)
	httpMsg.Write(body)
	httpBlock := httpMsg.Bytes()

	var rec bytes.Buffer
	rec.WriteString("WARC/1.0\r\n")
	rec.WriteString("WARC-Type: response\r\n")
	rec.WriteString("WARC-Target-URI: http://example.org/a.pdf\r\n")
	if truncated != "" {
		fmt.Fprintf(&rec, "WARC-Truncated: %s\r\n", truncated)
	}
	rec.WriteString("Content-Type: application/http; msgtype=response\r\n")
	fmt.Fprintf(&rec, "Content-Length: %d\r\n", len(httpBlock))
	rec.WriteString("\r\n")
	rec.Write(httpBlock)
	rec.WriteString("\r\n\r\n")

	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	if _, err := w.Write(rec.Bytes()); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return gz.Bytes()
}

func Test_extractWARCPayload(t *testing.T) {
	t.Run("truncated capture is flagged, not read", func(t *testing.T) {
		// HTTP Content-Length says 9000 but only a few bytes are stored: the
		// classic crawl-time truncation shape. We must detect it via the
		// WARC-Truncated header rather than failing on the short body.
		raw := gzipWARCResponse(t, "time", 9000, []byte("%PDF-1.4 partial"))
		_, err := extractWARCPayload(raw)
		if !errors.Is(err, ErrTruncatedCapture) {
			t.Fatalf("want ErrTruncatedCapture, got %v", err)
		}
	})

	t.Run("well-formed record returns body", func(t *testing.T) {
		body := []byte("%PDF-1.4 complete body bytes")
		raw := gzipWARCResponse(t, "", len(body), body)
		got, err := extractWARCPayload(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("body mismatch: got %q want %q", got, body)
		}
	})
}

func Test_findExtIds(t *testing.T) {
	cs := []struct {
		name   string
		header *tei.GrobidBiblio
		want   [][]string
	}{
		{
			name:   "no external ids",
			header: &tei.GrobidBiblio{},
			want:   [][]string{},
		},
		{
			name:   "doi only",
			header: &tei.GrobidBiblio{DOI: "10.1234/foo"},
			want:   [][]string{{"doi", "10.1234/foo"}},
		},
		{
			name:   "pmid only",
			header: &tei.GrobidBiblio{PMID: "12345678"},
			want:   [][]string{{"pmid", "12345678"}},
		},
		{
			name:   "pmcid only",
			header: &tei.GrobidBiblio{PMCID: "PMC1234567"},
			want:   [][]string{{"pmcid", "PMC1234567"}},
		},
		{
			// the struct field is ArxivID but findExtIds keys it as "arxiv"
			name:   "arxiv only",
			header: &tei.GrobidBiblio{ArxivID: "2301.00001"},
			want:   [][]string{{"arxiv", "2301.00001"}},
		},
		{
			name: "doi and arxiv, doi ranks first",
			header: &tei.GrobidBiblio{
				DOI:     "10.1234/foo",
				ArxivID: "2301.00001",
			},
			want: [][]string{
				{"doi", "10.1234/foo"},
				{"arxiv", "2301.00001"},
			},
		},
		{
			// fields set in reverse-priority order to prove the output is
			// ordered by id priority, not by which fields happen to be present.
			name: "pmid and pmcid stay in priority order",
			header: &tei.GrobidBiblio{
				PMCID: "PMC1234567",
				PMID:  "12345678",
			},
			want: [][]string{
				{"pmid", "12345678"},
				{"pmcid", "PMC1234567"},
			},
		},
		{
			name: "all four ids present, in priority order",
			header: &tei.GrobidBiblio{
				DOI:     "10.1234/foo",
				PMID:    "12345678",
				PMCID:   "PMC1234567",
				ArxivID: "2301.00001",
			},
			want: [][]string{
				{"doi", "10.1234/foo"},
				{"pmid", "12345678"},
				{"pmcid", "PMC1234567"},
				{"arxiv", "2301.00001"},
			},
		},
	}

	for _, c := range cs {
		t.Run(c.name, func(t *testing.T) {
			got := findExtIds(tei.GrobidDocument{Header: c.header})
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("findExtIds() = %#v, want %#v", got, c.want)
			}
		})
	}
}
