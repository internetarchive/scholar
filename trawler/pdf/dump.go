package pdf

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/miku/grobidclient/tei"
)

type DumpedPDF struct {
	Grobid  tei.GrobidDocument
	PdfText string
}

// Dump runs one PDF on disk through the pipeline and returns what came out, for
// eyeballing. It uses a Processor with no derivative store, so inspecting a PDF
// does not write anything to S3.
func Dump(pdfPath string) (*DumpedPDF, error) {
	pdfBs, err := os.ReadFile(pdfPath)
	if err != nil {
		return nil, err
	}

	content, err := NewProcessorWithoutStore().Process(context.Background(), pdfBs, "")
	if err != nil {
		return nil, err
	}

	if len(content.GrobidXML) == 0 {
		return nil, fmt.Errorf("no tei for %s: grobid outcome %q %s",
			pdfPath, content.GrobidOutcome, content.GrobidRejection)
	}

	gdoc, err := tei.ParseDocument(bytes.NewReader(content.GrobidXML))
	if err != nil {
		return nil, fmt.Errorf("failed to parse grobid xml: %w", err)
	}

	return &DumpedPDF{
		Grobid:  *gdoc,
		PdfText: string(content.PdfText),
	}, nil
}
