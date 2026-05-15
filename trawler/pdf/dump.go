package pdf

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/internetarchive/scholar/blobproc"
	"github.com/miku/grobidclient"
	"github.com/miku/grobidclient/tei"
)

type DumpedPDF struct {
	Grobid  tei.GrobidDocument
	PdfText string
}

func Dump(pdfPath string) (*DumpedPDF, error) {
	fi, err := os.Stat(pdfPath)
	if err != nil {
		return nil, err
	}

	grobid := grobidclient.New("https://scholar.archive.org/_grobid")

	l := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	params := blobproc.ProcessPDFParams{
		Path:              pdfPath,
		Size:              fi.Size(),
		Grobid:            grobid,
		GrobidMaxFileSize: 100 << 20,
		Logger:            l,
	}

	result, errs := blobproc.ProcessPDF(context.Background(), params)
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "encountered %d errors\n", len(errs))
		for _, err := range errs {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	gdoc, err := tei.ParseDocument(bytes.NewReader(result.TEI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse grobid xml: %w", err)
	}

	fmt.Printf("DBG %#v\n", string(gdoc.Header.DOI))

	// TODO fill in this struct
	return &DumpedPDF{}, nil
}
