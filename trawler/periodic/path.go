// Reserved for future one-off PDF ingestion tooling alongside UploadPDF.
// The periodic-ingest workflow no longer reads PDFs from SeaweedFS, so this
// helper has no callers in the periodic pipeline today.
package periodic

import "fmt"

// seaweedKey returns the SeaweedFS s3key for a PDF blob, e.g.
// "sandcrawler/pdf/ff/ff/ffff8adff053e3aef1f43eea6bdfab3f19c990a7.pdf".
// Caller must pass a 40-char hex SHA-1.
func seaweedKey(sha1hex string) string {
	return fmt.Sprintf("sandcrawler/pdf/%s/%s/%s.pdf", sha1hex[0:2], sha1hex[2:4], sha1hex)
}
