package pdfinfo

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/gen2brain/go-fitz"
	"github.com/pdfcpu/pdfcpu/pkg/api"
)

// Metadata groups output of various PDF inspection backends into a single struct.
type Metadata struct {
	PDFCPU  *PDFCPU `json:"pdfcpu,omitempty"`  // structural fields, via pdfcpu Go API.
	PDFInfo *Info   `json:"pdfinfo,omitempty"` // info-dict fields, via go-fitz (MuPDF).
}

// LegacyPDFExtra returns a struct that looks like the pdfextra dict from the
// sandcrawler. Here for compatibility.
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
type PDFExtra struct {
	Page0Height float64 `json:"page0height,omitempty"`  // in pts.
	Page0Width  float64 `json:"page0width,omitempty"`   // in pts.
	PageCount   int     `json:"page_count,omitempty"`
	PermanentID string  `json:"permanent_id,omitempty"` // TODO: where do we get this from?
	UpdateID    string  `json:"update_id,omitempty"`    // TODO: where do we get this from?
	PDFVersion  string  `json:"pdf_version,omitempty"`
}

// PDFCPU mirrors the historical "pdfcpu info -j" JSON shape so downstream
// consumers of blobproc's output don't need to change. Populated from
// pdfcpu's Go API; Header is left empty.
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

// Info is the per-PDF metadata struct. Historically populated by parsing
// Poppler "pdfinfo" output; now populated from go-fitz (MuPDF). Fields fitz
// cannot determine (CustomMetadata, MetadataStream, Suspects, JavaScript,
// Optimized, PDFSubtype, PageRot, Standard, Conformance, etc.) are left
// zero-valued and elided from JSON via omitempty.
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

// PageDim parses pdfinfo-style page size output into a Dim. Returns the zero
// value Dim for unparsable data.
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

// ParseBlob returns structured metadata for an in-memory PDF. Uses go-fitz
// (MuPDF) for the info-dict + basic geometry and pdfcpu's Go API for the
// richer structural fields. No external binaries required.
func ParseBlob(ctx context.Context, blob []byte) (*Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := infoFromFitz(blob)
	if err != nil {
		slog.Warn("fitz metadata extraction failed", "err", err)
		return nil, err
	}
	cpu, err := infoFromPdfcpu(blob)
	if err != nil {
		slog.Warn("pdfcpu metadata extraction failed", "err", err)
		return nil, err
	}
	// Prefer pdfcpu's float page dimensions for PageSize when available; fall
	// back to fitz's integer page bounds otherwise.
	if len(cpu.Infos) > 0 && len(cpu.Infos[0].PageSizes) > 0 {
		ps := cpu.Infos[0].PageSizes[0]
		info.PageSize = fmt.Sprintf("%v x %v pts", ps.Width, ps.Height)
	}
	return &Metadata{PDFInfo: info, PDFCPU: cpu}, nil
}

func infoFromFitz(blob []byte) (*Info, error) {
	doc, err := fitz.NewFromMemory(blob)
	if err != nil {
		return nil, fmt.Errorf("fitz open: %w", err)
	}
	defer doc.Close()
	// go-fitz's purego Metadata() returns each value as the raw 256-byte
	// lookup buffer (null-padded). Trim null bytes from every entry.
	m := doc.Metadata()
	for k, v := range m {
		m[k] = strings.TrimRight(v, "\x00")
	}
	info := &Info{
		Title:        m["title"],
		Subject:      m["subject"],
		Keywords:     m["keywords"],
		Author:       m["author"],
		Creator:      m["creator"],
		Producer:     m["producer"],
		CreationDate: m["creationDate"],
		ModDate:      m["modDate"],
		Pages:        doc.NumPage(),
		Encrypted:    m["encryption"] != "" && !strings.EqualFold(m["encryption"], "None"),
		PDFVersion:   strings.TrimPrefix(m["format"], "PDF "),
		FileSize:     len(blob),
	}
	if doc.NumPage() > 0 {
		if b, err := doc.Bound(0); err == nil {
			info.PageSize = fmt.Sprintf("%d x %d pts", b.Dx(), b.Dy())
		}
	}
	return info, nil
}

func infoFromPdfcpu(blob []byte) (*PDFCPU, error) {
	info, err := api.PDFInfo(bytes.NewReader(blob), "", nil, false, nil)
	if err != nil {
		return nil, err
	}
	cpuInfo := PDFCPUInfo{
		AppendOnly:         info.AppendOnly,
		Author:             info.Author,
		Bookmarks:          info.Outlines,
		CreationDate:       info.CreationDate,
		Creator:            info.Creator,
		Encrypted:          info.Encrypted,
		Form:               info.Form,
		Hybrid:             info.Hybrid,
		Keywords:           info.Keywords,
		Linearized:         info.Linearized,
		ModificationDate:   info.ModificationDate,
		Names:              info.Names,
		PageCount:          int64(info.PageCount),
		PageMode:           info.PageMode,
		Permissions:        int64(info.Permissions),
		Producer:           info.Producer,
		Signatures:         info.Signatures,
		Source:             info.FileName,
		Subject:            info.Subject,
		Tagged:             info.Tagged,
		Thumbnails:         info.Thumbnails,
		Title:              info.Title,
		Unit:               info.UnitString,
		UsingObjectStreams: info.UsingObjectStreams,
		UsingXRefStreams:   info.UsingXRefStreams,
		Version:            info.Version,
		Watermarked:        info.Watermarked,
	}
	// pdfcpu's Go API populates the PageDimensions set, not the Dimensions
	// slice (which is filled by the CLI right before JSON marshal).
	for d := range info.PageDimensions {
		cpuInfo.PageSizes = append(cpuInfo.PageSizes, struct {
			Height float64 `json:"height,omitempty"`
			Width  float64 `json:"width,omitempty"`
		}{Height: d.Height, Width: d.Width})
	}
	if v, ok := info.Properties["PTEX.Fullbanner"]; ok {
		cpuInfo.Properties.PTEXFullbanner = v
	}
	return &PDFCPU{Infos: []PDFCPUInfo{cpuInfo}}, nil
}
