package pdf

import (
	"bytes"
	"context"
	"crypto/sha1"
	"fmt"
	"net/http"
	"os"

	"github.com/miku/grobidclient/tei"
)

/*
	this file houses code for dumping structured data about PDFs. at time of
	initial writing it is intended to be used for debugging as we work on
	ingesting of PDFs from periodic crawls.

	We could submit files directly to grobid but will for now continue to use
	blobproc in case we want to migrate off of grobid in the near future.
*/

type DumpedPDF struct {
	Grobid  tei.GrobidDocument
	PdfText string
}

func Dump(pdfPath string) (*DumpedPDF, error) {
	processor := Processor{
		Client: &http.Client{},
	}
	ctx := context.Background()
	pdfBs, err := os.ReadFile(pdfPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read pdf '%s': %w", pdfPath, err)
	}

	headEnd := 1024
	if len(pdfBs) < headEnd {
		headEnd = len(pdfBs)
	}
	if !bytes.Contains(pdfBs[:headEnd], []byte("%PDF-")) {
		return nil, fmt.Errorf("file '%s' does not look like a PDF: missing %%PDF- header in first %d bytes", pdfPath, headEnd)
	}

	tailStart := len(pdfBs) - 1024
	if tailStart < 0 {
		tailStart = 0
	}
	if !bytes.Contains(pdfBs[tailStart:], []byte("%%EOF")) {
		return nil, fmt.Errorf("file '%s' appears truncated: missing %%EOF trailer", pdfPath)
	}

	sha := fmt.Sprintf("%x", sha1.Sum(pdfBs))
	fmt.Printf("DBG %#v\n", sha)
	content, err := processor.Process(ctx, pdfBs, sha)
	if err != nil {
		return nil, fmt.Errorf("failed to processPDF '%s': %w", pdfPath, err)
	}
	fmt.Println(content.GrobidXML)

	gdoc, err := tei.ParseDocument(bytes.NewReader(content.GrobidXML))
	if err != nil {
		return nil, fmt.Errorf("failed to parse grobid xml: %w", err)
	}

	return &DumpedPDF{
		Grobid:  *gdoc,
		PdfText: string(content.PdfText),
	}, nil
}
