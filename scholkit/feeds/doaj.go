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

// DOAJRecord is the serializable flat representation of a DOAJ OAI-PMH article
// record. Each line in the NDJSON output files is one JSON-encoded DOAJRecord.
type DOAJRecord struct {
	Identifier      string      `json:"identifier"`       // e.g. "oai:doaj.org/article:abc123..."
	Status          string      `json:"status"`           // "deleted" or ""
	Datestamp       string      `json:"datestamp"`        // "2023-01-15"
	SetSpec         []string    `json:"set_spec"`
	ID              string      `json:"id"`               // 32-char DOAJ article ID
	Title           string      `json:"title"`
	Publisher       string      `json:"publisher"`
	JournalTitle    string      `json:"journal_title"`
	ISSN            string      `json:"issn"`
	EISSN           string      `json:"eissn"`
	PublicationDate string      `json:"publication_date"` // "2023-01-15"
	Volume          string      `json:"volume"`
	Issue           string      `json:"issue"`
	StartPage       string      `json:"start_page"`
	EndPage         string      `json:"end_page"`
	DOI             string      `json:"doi"`
	Language        string      `json:"language"`
	Abstract        string      `json:"abstract"`
	Authors         []DOAJAuthor `json:"authors"`
	Keywords        []string    `json:"keywords"`
	FullTextURL     string      `json:"full_text_url"`
	FullTextFormat  string      `json:"full_text_format"`
	LicenseRef      string      `json:"license_ref"`
}

// DOAJAuthor holds an author's name, affiliation, and optional ORCID.
type DOAJAuthor struct {
	Name        string `json:"name"`
	Affiliation string `json:"affiliation"`
	OrcidID     string `json:"orcid_id"`
}

// doajNS is the XML namespace URI for the oai_doaj metadata format.
const doajNS = "http://doaj.org/features/oai_doaj/1.0/"

// oaiDoajMeta is used to XML-unmarshal the inner body of a metha.Record when
// the OAI metadata prefix is "oai_doaj". The root element is
// <oai_doaj:doajArticle xmlns:oai_doaj="http://doaj.org/features/oai_doaj/1.0/">,
// so all tags must include the namespace URI.
type oaiDoajMeta struct {
	XMLName         xml.Name `xml:"http://doaj.org/features/oai_doaj/1.0/ doajArticle"`
	Language        string   `xml:"http://doaj.org/features/oai_doaj/1.0/ language"`
	Publisher       string   `xml:"http://doaj.org/features/oai_doaj/1.0/ publisher"`
	JournalTitle    string   `xml:"http://doaj.org/features/oai_doaj/1.0/ journalTitle"`
	ISSN            string   `xml:"http://doaj.org/features/oai_doaj/1.0/ issn"`
	EISSN           string   `xml:"http://doaj.org/features/oai_doaj/1.0/ eissn"`
	PublicationDate string   `xml:"http://doaj.org/features/oai_doaj/1.0/ publicationDate"`
	Volume          string   `xml:"http://doaj.org/features/oai_doaj/1.0/ volume"`
	Issue           string   `xml:"http://doaj.org/features/oai_doaj/1.0/ issue"`
	StartPage       string   `xml:"http://doaj.org/features/oai_doaj/1.0/ startPage"`
	EndPage         string   `xml:"http://doaj.org/features/oai_doaj/1.0/ endPage"`
	DOI             string   `xml:"http://doaj.org/features/oai_doaj/1.0/ doi"`
	Title           string   `xml:"http://doaj.org/features/oai_doaj/1.0/ title"`
	Abstract        string   `xml:"http://doaj.org/features/oai_doaj/1.0/ abstract"`
	FullTextURL     struct {
		Format string `xml:"format,attr"`
		URL    string `xml:",chardata"`
	} `xml:"http://doaj.org/features/oai_doaj/1.0/ fullTextUrl"`
	Keywords struct {
		Keyword []string `xml:"http://doaj.org/features/oai_doaj/1.0/ keyword"`
	} `xml:"http://doaj.org/features/oai_doaj/1.0/ keywords"`
	Authors struct {
		Author []struct {
			Name          string `xml:"http://doaj.org/features/oai_doaj/1.0/ name"`
			AffiliationID string `xml:"http://doaj.org/features/oai_doaj/1.0/ affiliationId"`
			OrcidID       string `xml:"http://doaj.org/features/oai_doaj/1.0/ orcid_id"`
		} `xml:"http://doaj.org/features/oai_doaj/1.0/ author"`
	} `xml:"http://doaj.org/features/oai_doaj/1.0/ authors"`
	AffiliationsList struct {
		AffiliationName []struct {
			ID   string `xml:"affiliationId,attr"`
			Name string `xml:",chardata"`
		} `xml:"http://doaj.org/features/oai_doaj/1.0/ affiliationName"`
	} `xml:"http://doaj.org/features/oai_doaj/1.0/ affiliationsList"`
	LicenseRef string `xml:"http://doaj.org/features/oai_doaj/1.0/ licenseRef"`
}

// flattenDOAJRecord converts a metha.Record into a DOAJRecord with structured
// fields suitable for JSON serialization.
func flattenDOAJRecord(r *metha.Record) *DOAJRecord {
	dr := &DOAJRecord{
		Identifier: r.Header.Identifier,
		Status:     r.Header.Status,
		Datestamp:  r.Header.DateStamp,
		SetSpec:    r.Header.SetSpec,
		ID:         doajArticleID(r.Header.Identifier),
	}
	if len(r.Metadata.Body) == 0 {
		return dr
	}
	var meta oaiDoajMeta
	if err := xml.Unmarshal(r.Metadata.Body, &meta); err != nil {
		log.Printf("doaj: could not unmarshal metadata for %s: %v", r.Header.Identifier, err)
		return dr
	}

	// build affiliation ID → name map
	affMap := make(map[string]string)
	for _, a := range meta.AffiliationsList.AffiliationName {
		affMap[a.ID] = strings.TrimSpace(a.Name)
	}

	dr.Language = strings.TrimSpace(meta.Language)
	dr.Publisher = strings.TrimSpace(meta.Publisher)
	dr.JournalTitle = strings.TrimSpace(meta.JournalTitle)
	dr.ISSN = strings.TrimSpace(meta.ISSN)
	dr.EISSN = strings.TrimSpace(meta.EISSN)
	dr.PublicationDate = strings.TrimSpace(meta.PublicationDate)
	dr.Volume = strings.TrimSpace(meta.Volume)
	dr.Issue = strings.TrimSpace(meta.Issue)
	dr.StartPage = strings.TrimSpace(meta.StartPage)
	dr.EndPage = strings.TrimSpace(meta.EndPage)
	dr.DOI = strings.TrimSpace(meta.DOI)
	dr.Title = strings.TrimSpace(meta.Title)
	dr.Abstract = strings.TrimSpace(meta.Abstract)
	dr.FullTextURL = strings.TrimSpace(meta.FullTextURL.URL)
	dr.FullTextFormat = strings.TrimSpace(meta.FullTextURL.Format)
	dr.LicenseRef = strings.TrimSpace(meta.LicenseRef)

	for _, kw := range meta.Keywords.Keyword {
		if kw = strings.TrimSpace(kw); kw != "" {
			dr.Keywords = append(dr.Keywords, kw)
		}
	}

	for _, a := range meta.Authors.Author {
		aff := affMap[a.AffiliationID]
		if aff == "" {
			aff = strings.TrimSpace(a.AffiliationID)
		}
		dr.Authors = append(dr.Authors, DOAJAuthor{
			Name:        strings.TrimSpace(a.Name),
			Affiliation: aff,
			OrcidID:     strings.TrimSpace(a.OrcidID),
		})
	}

	return dr
}

// doajArticleID extracts the 32-char article ID from a DOAJ OAI identifier
// of the form "oai:doaj.org/article:<id>".
func doajArticleID(identifier string) string {
	const prefix = "oai:doaj.org/article:"
	return strings.TrimPrefix(identifier, prefix)
}

const (
	doajDefaultBaseURL        = "https://www.doaj.org/oai.article"
	doajDefaultMetadataPrefix = "oai_doaj"
)

// DOAJHarvester fetches records from the DOAJ OAI-PMH endpoint and writes
// zstd-compressed NDJSON to disk, analogous to ArxivHarvester.
type DOAJHarvester struct {
	// BaseURL is the OAI-PMH endpoint; defaults to doajDefaultBaseURL.
	BaseURL string
	// RequestDelay is inserted between successive OAI requests.
	RequestDelay time.Duration
	// Client is the metha HTTP client; nil uses metha.DefaultClient.
	Client *metha.Client
}

func (h *DOAJHarvester) baseURL() string {
	if h.BaseURL != "" {
		return h.BaseURL
	}
	return doajDefaultBaseURL
}

func (h *DOAJHarvester) client() *metha.Client {
	if h.Client != nil {
		return h.Client
	}
	return metha.DefaultClient
}

// DaySliceKey returns the filename for a given day's slice and the start/end
// times used, mirroring ArxivHarvester.DaySliceKey.
func (h *DOAJHarvester) DaySliceKey(t time.Time, prefix string) (key string, start, end time.Time) {
	start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	end = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
	key = fmt.Sprintf("%sdoaj-%s-%s.json.zst",
		prefix, start.Format("2006-01-02"), end.Format("2006-01-02"))
	return
}

// WriteDaySlice atomically writes a zstd-compressed NDJSON file for all DOAJ
// records with datestamps on the given day. Idempotent once the file exists.
func (h *DOAJHarvester) WriteDaySlice(t time.Time, dir, prefix string) error {
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

// WriteSlice fetches all DOAJ OAI records with datestamps between from and
// until, writing NDJSON to w. Each line is a JSON-encoded DOAJRecord.
func (h *DOAJHarvester) WriteSlice(w io.Writer, from, until time.Time) error {
	enc := json.NewEncoder(w)
	client := h.client()
	req := &metha.Request{
		BaseURL:        h.baseURL(),
		Verb:           "ListRecords",
		MetadataPrefix: doajDefaultMetadataPrefix,
		From:           from.Format("2006-01-02"),
		Until:          until.Format("2006-01-02"),
	}
	var total int
	for {
		if h.RequestDelay > 0 {
			time.Sleep(h.RequestDelay)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("doaj oai request: %v", err)
		}
		if resp.Error.Code != "" {
			if resp.Error.Code == "noRecordsMatch" {
				log.Printf("doaj: no records match for %s – %s", from.Format("2006-01-02"), until.Format("2006-01-02"))
				break
			}
			return fmt.Errorf("doaj oai error: %v", resp.Error)
		}
		for i := range resp.ListRecords.Records {
			flat := flattenDOAJRecord(&resp.ListRecords.Records[i])
			if err := enc.Encode(flat); err != nil {
				return fmt.Errorf("doaj encode record: %v", err)
			}
		}
		total += len(resp.ListRecords.Records)
		log.Printf("doaj: %d records (total=%d) [%s – %s]",
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
	log.Printf("doaj: done, total=%d records for %s", total, from.Format("2006-01-02"))
	return nil
}
