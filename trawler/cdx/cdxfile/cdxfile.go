// cdxfile parses rollup CDX files as found in IA crawl items. Distinct from
// trawler/cdx/cdxclient, which parses responses from the wayback CDX search
// API. The rollup files use the classic textual CDX format with a spec line
// at the top declaring field order.
package cdxfile

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
)

// Row is one parsed entry from a rollup CDX file.
type Row struct {
	SURT       string
	Timestamp  string
	URL        string
	Mimetype   string
	StatusCode string
	Sha1Base32 string
	WarcPath   string
	WarcOffset int64
	WarcSize   int64
}

// WARCItemAndFile splits Row.WarcPath into (item identifier, filename within
// item). For rollup CDX rows the path is "<itemID>/<filename>". An empty
// item is returned if no slash is present.
func (r Row) WARCItemAndFile() (item, file string) {
	if i := strings.IndexByte(r.WarcPath, '/'); i > 0 {
		return r.WarcPath[:i], r.WarcPath[i+1:]
	}
	return "", r.WarcPath
}

// expectedSpec is the field-code sequence Parse understands. Reproduces the
// spec line literally found at the top of IA crawl rollup CDX files:
//
//	CDX N b a m s k r M S V g
//
// N=SURT, b=timestamp, a=url, m=mimetype, s=status, k=sha1-base32,
// r=redirect, M=meta, S=record size, V=offset, g=warc path.
var expectedSpec = []string{"CDX", "N", "b", "a", "m", "s", "k", "r", "M", "S", "V", "g"}

// PDFFilter passes through only PDF response records (200 status, mimetype
// application/pdf, non-empty WARC path).
var PDFFilter = func(r Row) bool {
	return r.Mimetype == "application/pdf" && r.StatusCode == "200" && r.WarcPath != ""
}

// Parse reads a rollup CDX (gunzipped automatically if input starts with
// gzip magic) and returns rows matching the filter. A nil filter passes
// everything. Per-row parse errors are logged and skipped.
func Parse(r io.Reader, filter func(Row) bool) ([]Row, error) {
	br := bufio.NewReader(r)
	if magic, err := br.Peek(2); err == nil && len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("gzip open: %w", err)
		}
		defer gz.Close()
		return parseText(gz, filter)
	}
	return parseText(br, filter)
}

func parseText(r io.Reader, filter func(Row) bool) ([]Row, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("scan failed before spec line: %w", err)
		}
		return nil, errors.New("empty CDX file")
	}
	specFields := strings.Fields(scanner.Text())
	if !slices.Equal(specFields, expectedSpec) {
		return nil, fmt.Errorf("unsupported CDX spec %v (expected %v)", specFields, expectedSpec)
	}

	var out []Row
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		row, err := parseRow(line)
		if err != nil {
			slog.Warn("cdxfile: skipping malformed row", "err", err, "line", line)
			continue
		}
		if filter != nil && !filter(row) {
			continue
		}
		out = append(out, row)
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("scan failed: %w", err)
	}
	return out, nil
}

func parseRow(line string) (Row, error) {
	fields := strings.Fields(line)
	if len(fields) != 11 {
		return Row{}, fmt.Errorf("expected 11 fields, got %d", len(fields))
	}
	size, err := strconv.ParseInt(fields[8], 10, 64)
	if err != nil {
		return Row{}, fmt.Errorf("invalid size %q: %w", fields[8], err)
	}
	offset, err := strconv.ParseInt(fields[9], 10, 64)
	if err != nil {
		return Row{}, fmt.Errorf("invalid offset %q: %w", fields[9], err)
	}
	return Row{
		SURT:       fields[0],
		Timestamp:  fields[1],
		URL:        fields[2],
		Mimetype:   fields[3],
		StatusCode: fields[4],
		Sha1Base32: fields[5],
		WarcPath:   fields[10],
		WarcOffset: offset,
		WarcSize:   size,
	}, nil
}
