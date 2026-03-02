package feeds

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/internetarchive/scholar/scholkit/atomicfile"
	"github.com/klauspost/compress/zstd"
	"github.com/miku/metha"
)

// ArxivRecord is the serializable flat representation of an OAI-PMH arXiv
// record. Each line in the NDJSON output files is one JSON-encoded ArxivRecord.
type ArxivRecord struct {
	Identifier string        `json:"identifier"` // e.g. "oai:arXiv.org:2301.12345"
	Status     string        `json:"status"`     // "deleted" or ""
	Datestamp  string        `json:"datestamp"`  // "2023-01-15"
	SetSpec    []string      `json:"set_spec"`
	ID         string        `json:"id"`         // "2301.12345" (no version suffix)
	Title      string        `json:"title"`
	Authors    []ArxivAuthor `json:"authors"`
	Categories string        `json:"categories"` // space-separated, e.g. "cs.AI cs.LG"
	Abstract   string        `json:"abstract"`
	DOI        string        `json:"doi"`
	Created    string        `json:"created"` // "2007-04-02"
	Updated    string        `json:"updated"`
	Comments   string        `json:"comments"`
	License    string        `json:"license"`
	JournalRef string        `json:"journal_ref"`
	ReportNo   string        `json:"report_no"`
}

// ArxivAuthor holds parsed author name components from the arXiv OAI format.
type ArxivAuthor struct {
	KeyName     string `json:"keyname"`
	ForeName    string `json:"forename"`
	Suffix      string `json:"suffix"`
	Affiliation string `json:"affiliation"`
}

// arXivMeta is used to XML-unmarshal the inner body of a metha.Record when
// the OAI metadata prefix is "arXiv".
type arXivMeta struct {
	XMLName    xml.Name `xml:"arXiv"`
	ID         string   `xml:"id"`
	Title      string   `xml:"title"`
	Authors    struct {
		Author []struct {
			KeyName     string `xml:"keyname"`
			ForeName    string `xml:"forenames"`
			Suffix      string `xml:"suffix"`
			Affiliation string `xml:"affiliation"`
		} `xml:"author"`
	} `xml:"authors"`
	Categories string `xml:"categories"`
	Abstract   string `xml:"abstract"`
	DOI        string `xml:"doi"`
	Created    string `xml:"created"`
	Updated    string `xml:"updated"`
	Comments   string `xml:"comments"`
	License    string `xml:"license"`
	JournalRef string `xml:"journal-ref"`
	ReportNo   string `xml:"report-no"`
}

// flattenArxivRecord converts a metha.Record into an ArxivRecord with
// structured fields suitable for JSON serialization.
func flattenArxivRecord(r *metha.Record) *ArxivRecord {
	ar := &ArxivRecord{
		Identifier: r.Header.Identifier,
		Status:     r.Header.Status,
		Datestamp:  r.Header.DateStamp,
		SetSpec:    r.Header.SetSpec,
	}
	if len(r.Metadata.Body) == 0 {
		return ar
	}
	var meta arXivMeta
	if err := xml.Unmarshal(r.Metadata.Body, &meta); err != nil {
		log.Printf("arxiv: could not unmarshal metadata for %s: %v", r.Header.Identifier, err)
		return ar
	}
	ar.ID = meta.ID
	ar.Title = strings.TrimSpace(meta.Title)
	ar.Categories = strings.TrimSpace(meta.Categories)
	ar.Abstract = strings.TrimSpace(meta.Abstract)
	ar.DOI = strings.TrimSpace(meta.DOI)
	ar.Created = meta.Created
	ar.Updated = meta.Updated
	ar.Comments = strings.TrimSpace(meta.Comments)
	ar.License = meta.License
	ar.JournalRef = strings.TrimSpace(meta.JournalRef)
	ar.ReportNo = strings.TrimSpace(meta.ReportNo)
	for _, a := range meta.Authors.Author {
		ar.Authors = append(ar.Authors, ArxivAuthor{
			KeyName:     a.KeyName,
			ForeName:    a.ForeName,
			Suffix:      a.Suffix,
			Affiliation: a.Affiliation,
		})
	}
	return ar
}

const (
	arxivDefaultBaseURL        = "https://export.arxiv.org/oai2"
	arxivDefaultMetadataPrefix = "arXiv"
)

// ArxivHarvester fetches records from the arXiv OAI-PMH endpoint and writes
// zstd-compressed NDJSON to disk, analogous to PubMedHarvester.
type ArxivHarvester struct {
	// BaseURL is the OAI-PMH endpoint; defaults to arxivDefaultBaseURL.
	BaseURL string
	// MetadataPrefix is the OAI metadata format; defaults to "arXiv".
	MetadataPrefix string
	// Set is an optional OAI set specifier (e.g. "cs" for computer science).
	Set string
	// RequestDelay is inserted between successive OAI requests. arXiv may
	// throttle aggressive crawlers; 0 means no delay.
	RequestDelay time.Duration
	// Client is the metha HTTP client; nil uses metha.DefaultClient.
	Client *metha.Client
}

func (h *ArxivHarvester) baseURL() string {
	if h.BaseURL != "" {
		return h.BaseURL
	}
	return arxivDefaultBaseURL
}

func (h *ArxivHarvester) metadataPrefix() string {
	if h.MetadataPrefix != "" {
		return h.MetadataPrefix
	}
	return arxivDefaultMetadataPrefix
}

func (h *ArxivHarvester) client() *metha.Client {
	if h.Client != nil {
		return h.Client
	}
	return metha.DefaultClient
}

// DaySliceKey returns the filename for a given day's slice and the start/end
// times used, mirroring PubMedHarvester.DaySliceKey.
func (h *ArxivHarvester) DaySliceKey(t time.Time, prefix string) (key string, start, end time.Time) {
	start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	end = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
	key = fmt.Sprintf("%sarxiv-%s-%s-%s.json.zst",
		prefix, h.metadataPrefix(), start.Format("2006-01-02"), end.Format("2006-01-02"))
	return
}

// WriteDaySlice atomically writes a zstd-compressed NDJSON file for all arXiv
// records with datestamps on the given day. Idempotent once the file exists.
func (h *ArxivHarvester) WriteDaySlice(t time.Time, dir, prefix string) error {
	fn, start, end := h.DaySliceKey(t, prefix)
	dst := path.Join(dir, fn)
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	f, err := atomicfile.New(dst)
	if err != nil {
		return err
	}
	enc, err := zstd.NewWriter(f)
	if err != nil {
		return err
	}
	if err := h.WriteSlice(enc, start, end); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return f.Close()
}

// WriteSlice fetches all arXiv OAI records with datestamps between from and
// until, writing NDJSON to w. Each line is a JSON-encoded ArxivRecord.
func (h *ArxivHarvester) WriteSlice(w io.Writer, from, until time.Time) error {
	enc := json.NewEncoder(w)
	client := h.client()
	req := &metha.Request{
		BaseURL:        h.baseURL(),
		Verb:           "ListRecords",
		MetadataPrefix: h.metadataPrefix(),
		From:           from.Format("2006-01-02"),
		Until:          until.Format("2006-01-02"),
		Set:            h.Set,
	}
	var total int
	for {
		if h.RequestDelay > 0 {
			time.Sleep(h.RequestDelay)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("arxiv oai request: %v", err)
		}
		if resp.Error.Code != "" {
			if resp.Error.Code == "noRecordsMatch" {
				log.Printf("arxiv: no records match for %s – %s", from.Format("2006-01-02"), until.Format("2006-01-02"))
				break
			}
			return fmt.Errorf("arxiv oai error: %v", resp.Error)
		}
		for i := range resp.ListRecords.Records {
			flat := flattenArxivRecord(&resp.ListRecords.Records[i])
			if err := enc.Encode(flat); err != nil {
				return fmt.Errorf("arxiv encode record: %v", err)
			}
		}
		total += len(resp.ListRecords.Records)
		log.Printf("arxiv: %d records (total=%d) [%s – %s]",
			len(resp.ListRecords.Records), total,
			from.Format("2006-01-02"), until.Format("2006-01-02"))
		token := resp.GetResumptionToken()
		if token == "" {
			break
		}
		req = &metha.Request{
			BaseURL:         h.baseURL(),
			Verb:            "ListRecords",
			ResumptionToken: token,
		}
	}
	log.Printf("arxiv: done, total=%d records for %s", total, from.Format("2006-01-02"))
	return nil
}
