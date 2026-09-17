package pdf

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/minio/minio-go/v7"
)

// The rederivable derivative store: text, thumbnail and TEI written to S3 under
// the PDF's sha1, where scholar's web UI and the reindex path read them.
//
// The layout is not ours to choose. These keys have been served for years and
// are read by systems that know nothing about trawler, so every one of them is
// reproduced exactly as blobproc wrote it -- bucket, folder, fan-out and
// extension. derivativePath is the one place that shape is expressed, and
// TestDerivativePath pins it.
//
// Two things blobproc did per put are gone. It called BucketExists and created
// the bucket if missing, which is one or two extra round trips on every
// derivative of every PDF to discover something that has been true for years;
// buckets are now expected to exist, and a put to a missing one fails loudly.
// It also ran mimetype.Detect over each blob to pick a content type, which is a
// scan of the bytes to rediscover what the caller already knew, so the content
// type is passed in. The values below are what the detector produced, so
// objects written now are indistinguishable from objects written before.

// derivative names a kind of output and where it lives.
type derivative struct {
	bucket      string
	folder      string
	ext         string
	contentType string
}

var (
	// textDerivative is the extracted plain text.
	textDerivative = derivative{
		bucket: "sandcrawler", folder: "text", ext: "txt",
		contentType: "text/plain; charset=utf-8",
	}
	// thumbnailDerivative is the page-0 JPEG. The extension encodes the width,
	// which is why thumbWidth is part of the derivative contract.
	thumbnailDerivative = derivative{
		bucket: "thumbnail", folder: "pdf", ext: "180px.jpg",
		contentType: "image/jpeg",
	}
	// teiDerivative is GROBID's TEI XML.
	teiDerivative = derivative{
		bucket: "sandcrawler", folder: "grobid", ext: "tei.xml",
		contentType: "text/xml; charset=utf-8",
	}
)

// expectedSHA1Length is the length of a hex-encoded sha1. A key built from
// anything else would land somewhere nobody looks for it.
const expectedSHA1Length = 40

// derivativePath is the object key for one derivative of one PDF, e.g.
//
//	grobid/ff/8a/ff8adff053e3aef1f43eea6bdfab3f19c990a7bc.tei.xml
//
// The two-byte fan-out keeps any one directory listable.
func derivativePath(folder, sha1hex, ext string) string {
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return fmt.Sprintf("%s/%s/%s/%s%s", folder, sha1hex[0:2], sha1hex[2:4], sha1hex, ext)
}

// putDerivative writes one derivative. A nil client means derivatives are not
// being stored, which is how `trawler pdf dump` uses the pipeline.
func putDerivative(ctx context.Context, mc *minio.Client, d derivative, sha1hex string, blob []byte) error {
	if mc == nil {
		return nil
	}
	if len(sha1hex) != expectedSHA1Length {
		return fmt.Errorf("refusing to store %s derivative under bad sha1 %q", d.folder, sha1hex)
	}

	path := derivativePath(d.folder, sha1hex, d.ext)
	info, err := mc.PutObject(ctx, d.bucket, path, bytes.NewReader(blob), int64(len(blob)),
		minio.PutObjectOptions{ContentType: d.contentType})
	if err != nil {
		return fmt.Errorf("could not store %s/%s: %w", d.bucket, path, err)
	}

	// Paranoia carried over from blobproc: something between us and the disk
	// rewriting the key would put the derivative where no reader will look for
	// it, and silently.
	if info.Bucket != d.bucket || info.Key != path {
		return fmt.Errorf("stored %s/%s but was told it landed at %s/%s",
			d.bucket, path, info.Bucket, info.Key)
	}
	return nil
}
