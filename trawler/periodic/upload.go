// Reserved for future one-off PDF ingestion tooling. The periodic-ingest
// workflow reads PDF bytes directly from petabox via Range GETs; nothing in
// the periodic pipeline calls UploadPDF today. Kept in place so a later
// "trawler pdf ingest-one <file>" command can build on it.
package periodic

import (
	"context"
	"crypto/sha1"
	"fmt"

	"git.archive.org/webgroup/scholar/trawler/s3"
)

// UploadPDF computes the SHA-1 of pdfBs, uploads it to SeaweedFS at the
// fragmented-SHA1 PDF path, and returns the hex digest.
func UploadPDF(ctx context.Context, pdfBs []byte) (string, error) {
	h := sha1.New()
	h.Write(pdfBs)
	sha1hex := fmt.Sprintf("%x", h.Sum(nil))

	if err := s3.PutObject(ctx, seaweedKey(sha1hex), pdfBs, "application/pdf"); err != nil {
		return "", fmt.Errorf("seaweed upload failed for %s: %w", sha1hex, err)
	}
	return sha1hex, nil
}
