package pdfinfo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Metadata groups output of various tools into a single struct.
type Metadata struct {
	PDFCPU  *PDFCPU `json:"pdfcpu,omitempty"`  // pdfcpu output, parsed into JSON.
	PDFInfo *Info   `json:"pdfinfo,omitempty"` // pdfinfo, parsed into JSON.
}

// LegacyPDFExtra returns a struct that looks like the pdfextra dict from the
// sandcrawler. Here for compatibilty.
func (metadata Metadata) LegacyPDFExtra() *PDFExtra {
	return &PDFExtra{
		Page0Height: metadata.PDFInfo.PageDim().Height,
		Page0Width:  metadata.PDFInfo.PageDim().Width,
		PageCount:   metadata.PDFInfo.Pages,
		PDFVersion:  metadata.PDFInfo.PDFVersion,
	}
}

// PDFExtra was a free form dictionary in sandcrawler. Keep this here for
// compatibility.
//
// In [10]: pdf_document.pdf_id
// Out[10]: PDFId(permanent_id='070262676b9d8a3776b3a9e2c168f961',
// update_id='29245f594c8bea0fc7f2cc90ca1dd021')
type PDFExtra struct {
	Page0Height float64 `json:"page0height,omitempty"`  // in pts, we can parse "pdfinfo" output
	Page0Width  float64 `json:"page0width,omitempty"`   // in pts, we can parse "pdfinfo" output
	PageCount   int     `json:"page_count,omitempty"`   // "pdfinfo" "Pages"
	PermanentID string  `json:"permanent_id,omitempty"` // TODO: where do we get this from?
	UpdateID    string  `json:"update_id,omitempty"`    // TODO: where do we get this from?
	PDFVersion  string  `json:"pdf_version,omitempty"`  // PDF version: 1.5, ...
}

// PDFCPU structured output from pdfcpu tool. One annoyance of pdfcpu is that
// it expect the file to have a .pdf extenstion (that's sooo weird!).
type PDFCPU struct {
	Header struct {
		Creation string `json:"creation,omitempty"`
		Version  string `json:"version,omitempty"`
	} `json:"header,omitempty"`
	Infos []PDFCPUInfo `json:"infos,omitempty"`
}

type PDFCPUInfo struct {
	AppendOnly       bool     `json:"appendOnly,omitempty"`
	Author           string   `json:"author,omitempty"`
	Bookmarks        bool     `json:"bookmarks,omitempty"`
	CreationDate     string   `json:"creationDate,omitempty"`
	Creator          string   `json:"creator,omitempty"`
	Encrypted        bool     `json:"encrypted,omitempty"`
	Form             bool     `json:"form,omitempty"`
	Hybrid           bool     `json:"hybrid,omitempty"`
	Keywords         []string `json:"keywords,omitempty"`
	Linearized       bool     `json:"linearized,omitempty"`
	ModificationDate string   `json:"modificationDate,omitempty"`
	Names            bool     `json:"names,omitempty"`
	PageCount        int64    `json:"pageCount,omitempty"`
	PageMode         string   `json:"pageMode,omitempty"`
	PageSizes        []struct {
		Height float64 `json:"height,omitempty"`
		Width  float64 `json:"width,omitempty"`
	} `json:"pageSizes,omitempty"`
	Permissions int64  `json:"permissions,omitempty"`
	Producer    string `json:"producer,omitempty"`
	Properties  struct {
		PTEXFullbanner string `json:"PTEX.Fullbanner,omitempty"`
	} `json:"properties,omitempty"`
	Signatures         bool   `json:"signatures,omitempty"`
	Source             string `json:"source,omitempty"`
	Subject            string `json:"subject,omitempty"`
	Tagged             bool   `json:"tagged,omitempty"`
	Thumbnails         bool   `json:"thumbnails,omitempty"`
	Title              string `json:"title,omitempty"`
	Unit               string `json:"unit,omitempty"`
	UsingObjectStreams bool   `json:"usingObjectStreams,omitempty"`
	UsingXRefStreams   bool   `json:"usingXRefStreams,omitempty"`
	Version            string `json:"version,omitempty"`
	Watermarked        bool   `json:"watermarked,omitempty"`
}

// Info is a parsed pdfinfo output.
type Info struct {
	Title          string `json:"title,omitempty"`
	Subject        string `json:"subject,omitempty"`
	Keywords       string `json:"keywords,omitempty"`
	Author         string `json:"author,omitempty"`
	Creator        string `json:"creator,omitempty"`
	Producer       string `json:"producer,omitempty"`
	CreationDate   string `json:"creation_date,omitempty"`
	ModDate        string `json:"mod_date,omitempty"`
	CustomMetadata bool   `json:"custom_metadata,omitempty"`
	MetadataStream bool   `json:"metadata_stream,omitempty"`
	Tagged         bool   `json:"tagged,omitempty"`
	UserProperties bool   `json:"user_properties,omitempty"`
	Suspects       bool   `json:"suspects,omitempty"`
	Form           string `json:"form,omitempty"`
	JavaScript     bool   `json:"javascript,omitempty"`
	Pages          int    `json:"pages,omitempty"`
	Encrypted      bool   `json:"encrypted,omitempty"`
	PageSize       string `json:"page_size,omitempty"`
	PageRot        int    `json:"page_rot,omitempty"`
	FileSize       int    `json:"filesize,omitempty"`
	Optimized      bool   `json:"optimized,omitempty"`
	PDFVersion     string `json:"pdf_version,omitempty"`
	PDFSubtype     string `json:"pdf_subtype,omitempty"`
	Abbreviation   string `json:"abbreviation,omitempty"`
	Subtitle       string `json:"subtitle,omitempty"`
	Standard       string `json:"standard,omitempty"`
	Conformance    string `json:"conformance,omitempty"`
}

// Dim groups width and height of a page.
type Dim struct {
	Width  float64
	Height float64
}

// PageDim parses pdfinfo page size output into a Dim. Returns the zero value
// Dim for unparsable data.
func (info *Info) PageDim() Dim {
	if info == nil {
		return Dim{}
	}
	var (
		// 463.059 x 668.047 pts
		// 595 x 882 pts
		re            = regexp.MustCompile(`(?<width>[0-9.]*)[\s]*x[\s]*(?<height>[0-9.]*)`)
		matches       = re.FindStringSubmatch(info.PageSize)
		width, height float64
		err           error
	)
	if len(matches) < 3 {
		return Dim{}
	}
	if width, err = strconv.ParseFloat(matches[re.SubexpIndex("width")], 64); err != nil {
		return Dim{}
	}
	if height, err = strconv.ParseFloat(matches[re.SubexpIndex("height")], 64); err != nil {
		return Dim{}
	}
	return Dim{
		Width:  width,
		Height: height,
	}
}

// ParseFile a filename into a structured metadata object. Requires pdfinfo and
// pdfcpu to be installed. The filename must have .pdf extension, otherwise
// pdfcpu will fail.
func ParseFile(ctx context.Context, filename string) (*Metadata, error) {
	if !strings.HasSuffix(filename, ".pdf") {
		return nil, fmt.Errorf("pdfcpu requires an explicit .pdf filename")
	}
	if _, err := exec.LookPath("pdfcpu"); err != nil {
		return nil, fmt.Errorf("missing pdfcpu executable, cf. https://github.com/pdfcpu/pdfcpu")
	}
	if _, err := exec.LookPath("pdfinfo"); err != nil {
		return nil, fmt.Errorf("missing pdfinfo executable")
	}
	var metadata = new(Metadata)
	info, err := runPdfInfo(ctx, filename)
	if err != nil {
		return nil, err
	}
	metadata.PDFInfo = info
	pdfcpu, err := runPdfCpu(ctx, filename)
	if err != nil {
		return nil, err
	}
	metadata.PDFCPU = pdfcpu
	return metadata, nil
}

// runPdfCpu parses a pdf file. Requires pdfcpu executable to be installed.
// The filename must have .pdf extension, otherwise pdfcpu will fail.  If the
// first attempt fails, a second attempt is made before returning an error.
func runPdfCpu(ctx context.Context, filename string) (*PDFCPU, error) {
	const maxAttempts = 2
	var lastErr error

	for try := 1; try <= maxAttempts; try++ {
		var buf bytes.Buffer
		cmd := exec.CommandContext(ctx, "pdfcpu", "info", "-j", filename)
		cmd.Stdout = &buf
		if err := cmd.Run(); err != nil {
			lastErr = err
			continue
		}
		var pdfcpu PDFCPU
		if err := json.Unmarshal(buf.Bytes(), &pdfcpu); err != nil {
			lastErr = err
			continue
		}
		return &pdfcpu, nil
	}
	return nil, lastErr
}

// runPdfInfo parses a pdf file. Requires pdfinfo executable to be installed.
func runPdfInfo(ctx context.Context, filename string) (*Info, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "pdfinfo", filename)
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return ParseInfo(buf.String()), nil
}

// ParseInfo pdfinfo output into an Info struct.
func ParseInfo(s string) *Info {
	info := Info{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		fields := strings.SplitN(line, ":", 2)
		if len(fields) != 2 {
			continue
		}
		fields[0] = strings.TrimSpace(fields[0])
		fields[1] = strings.TrimSpace(fields[1])
		switch fields[0] {
		case "Title":
			info.Title = fields[1]
		case "Subject":
			info.Subject = fields[1]
		case "Keywords":
			info.Keywords = fields[1]
		case "Author":
			info.Author = fields[1]
		case "Creator":
			info.Creator = fields[1]
		case "Producer":
			info.Producer = fields[1]
		case "CreationDate":
			info.CreationDate = fields[1]
		case "ModDate":
			info.ModDate = fields[1]
		case "Custom Metadata":
			info.CustomMetadata = parseBool(fields[1])
		case "Metadata Stream":
			info.MetadataStream = parseBool(fields[1])
		case "Tagged":
			info.Tagged = parseBool(fields[1])
		case "UserProperties":
			info.UserProperties = parseBool(fields[1])
		case "Suspects":
			info.Suspects = parseBool(fields[1])
		case "Form":
			info.Form = fields[1]
		case "JavaScript":
			info.JavaScript = parseBool(fields[1])
		case "Pages":
			info.Pages = parseInt(fields[1])
		case "Encrypted":
			info.Encrypted = parseBool(fields[1])
		case "Page size":
			info.PageSize = fields[1]
		case "Page rot":
			info.PageRot = parseInt(fields[1])
		case "File size":
			info.FileSize = parseAnyInt(fields[1])
		case "Optimized":
			info.Optimized = parseBool(fields[1])
		case "PDF version":
			info.PDFVersion = fields[1]
		case "PDF subtype":
			info.PDFSubtype = fields[1]
		case "Abbreviation":
			info.Abbreviation = fields[1]
		case "Subtitle":
			info.Subtitle = fields[1]
		case "Standard":
			info.Standard = fields[1]
		case "Conformance":
			info.Conformance = fields[1]
		default:
			log.Printf("ignoring pdfinfo field: %v", fields[0])
		}
	}
	return &info
}

// parseBool returns a bool from a string used in pdfinfo output, like "yes", and "no".
func parseBool(s string) bool {
	if s == "yes" {
		return true
	}
	return false
}

// parseInt return 0, if no other value could be parsed.
func parseInt(s string) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return v
}

// parseAnyInt will return the first token in a given string, that could be parsed into an int.
func parseAnyInt(s string) int {
	for _, tok := range strings.Fields(s) {
		v, err := strconv.Atoi(tok)
		if err != nil {
			continue
		}
		return v
	}
	return 0
}
