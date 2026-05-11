package blobproc

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/internetarchive/scholar/blobproc/pdfextract"
	"github.com/miku/grobidclient"
)

// ProcessPDFParams configures a single PDF processing run.
//
// Grobid and S3 are both optional; a nil client causes the corresponding
// derivative step to be logged and skipped. Logger defaults to slog.Default().
type ProcessPDFParams struct {
	Path              string
	Size              int64
	Grobid            *grobidclient.Grobid
	S3                *BlobStore
	GrobidMaxFileSize int64
	Logger            *slog.Logger
}

// ProcessPDFResult collects the derivatives extracted from a PDF. Library
// callers can use this directly (e.g. with S3 = nil) instead of (or in
// addition to) the S3 uploads ProcessPDF performs when an S3 client is
// configured. Any field may be empty if the corresponding step was skipped or
// failed; consult the returned errors for details.
type ProcessPDFResult struct {
	SHA1Hex   string // sha1 of the input file
	Thumbnail []byte // page-0 JPEG thumbnail
	Text      string // extracted plain text
	TEI       []byte // GROBID TEI XML body
}

// ProcessPDF runs the full per-file pipeline against a PDF on disk:
// pdfextract for text + page-0 thumbnail, then GROBID for structured TEI.
// When an S3 client is configured each derivative is uploaded to its
// conventional bucket/folder; the same data is also returned in the result so
// callers can use ProcessPDF as a library function. The returned result is
// always non-nil, with fields populated as they become available. The errors
// slice collects every error encountered; an empty (or nil) slice means the
// run was fully successful. The caller is responsible for stats accounting
// and removing the file from the spool.
func ProcessPDF(ctx context.Context, p ProcessPDFParams) (*ProcessPDFResult, []error) {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	out := &ProcessPDFResult{}
	var errs []error

	result := pdfextract.ProcessFile(ctx, p.Path, &pdfextract.Options{
		Dim:       pdfextract.Dim{180, 300},
		ThumbType: "JPEG",
	})
	switch {
	case result.Status != "success":
		logger.Warn("pdfextract failed", "path", p.Path, "status", result.Status, "err", result.Err)
		if result.Err != nil {
			errs = append(errs, result.Err)
		} else {
			errs = append(errs, fmt.Errorf("pdfextract failed with status %q", result.Status))
		}
	case len(result.SHA1Hex) != ExpectedSHA1Length:
		logger.Warn("invalid sha1 in response", "sha1", result.SHA1Hex)
		errs = append(errs, fmt.Errorf("invalid SHA1 in response: %v", result.SHA1Hex))
	default:
		out.SHA1Hex = result.SHA1Hex
		out.Text = result.Text
		if result.HasPage0Thumbnail() {
			out.Thumbnail = result.Page0Thumbnail
			if p.S3 == nil {
				logger.Debug("skipping S3 put (thumbnail), S3 client not available", "sha1", result.SHA1Hex)
			} else {
				resp, err := p.S3.PutBlob(ctx, &BlobRequestOptions{
					Bucket:  "thumbnail",
					Folder:  "pdf",
					Blob:    result.Page0Thumbnail,
					SHA1Hex: result.SHA1Hex,
					Ext:     "180px.jpg",
				})
				if err != nil {
					logger.Error("s3 failed (thumbnail)", "err", err, "sha1", result.SHA1Hex)
					errs = append(errs, fmt.Errorf("s3 failed (thumbnail): %v", result.SHA1Hex))
				} else {
					logger.Debug("s3 put ok", "bucket", resp.Bucket, "path", resp.ObjectPath)
				}
			}
		}
		if len(result.Text) > 0 {
			if p.S3 == nil {
				logger.Debug("skipping S3 put (text), S3 client not available", "sha1", result.SHA1Hex)
			} else {
				resp, err := p.S3.PutBlob(ctx, &BlobRequestOptions{
					Bucket:  "sandcrawler",
					Folder:  "text",
					Blob:    []byte(result.Text),
					SHA1Hex: result.SHA1Hex,
					Ext:     "txt",
				})
				if err != nil {
					logger.Error("s3 failed (text)", "err", err, "sha1", result.SHA1Hex)
					errs = append(errs, fmt.Errorf("s3 failed (text): %v", result.SHA1Hex))
				} else {
					logger.Debug("s3 put ok", "bucket", resp.Bucket, "path", resp.ObjectPath)
				}
			}
		}
	}

	if p.Grobid == nil {
		logger.Debug("skipping GROBID processing, GROBID client not available", "path", p.Path)
		return out, errs
	}
	if p.Size > p.GrobidMaxFileSize {
		logger.Warn("skipping too large file for GROBID", "path", p.Path, "size", p.Size)
		return out, errs
	}
	gres, err := p.Grobid.ProcessPDFContext(ctx, p.Path, "processFulltextDocument", &grobidclient.Options{
		GenerateIDs:            true,
		ConsolidateHeader:      true,
		ConsolidateCitations:   false, // "too expensive for now"
		IncludeRawCitations:    true,
		IncludeRawAffiliations: true,
		TEICoordinates:         []string{"ref", "figure", "persName", "formula", "biblStruct"},
		SegmentSentences:       true,
	})
	switch {
	case err != nil:
		logger.Warn("grobid failed", "err", err)
		errs = append(errs, err)
	case gres.Err != nil:
		logger.Warn("grobid failed", "err", gres.Err)
		errs = append(errs, gres.Err)
	default:
		out.TEI = gres.Body
		if out.SHA1Hex == "" {
			out.SHA1Hex = gres.SHA1Hex
		}
		if p.S3 == nil {
			logger.Debug("skipping S3 put (grobid), S3 client not available", "sha1", gres.SHA1Hex)
		} else {
			resp, err := p.S3.PutBlob(ctx, &BlobRequestOptions{
				Bucket:  "sandcrawler",
				Folder:  "grobid",
				Blob:    gres.Body,
				SHA1Hex: gres.SHA1Hex,
				Ext:     "tei.xml",
			})
			if err != nil {
				logger.Error("s3 failed (grobid)", "err", err)
				errs = append(errs, fmt.Errorf("s3 failed (grobid): %v", err))
			} else {
				logger.Debug("s3 put ok", "bucket", resp.Bucket, "path", resp.ObjectPath)
			}
		}
	}
	return out, errs
}
