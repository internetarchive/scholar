package blobproc

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/gabriel-vasile/mimetype"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var (
	ErrFileTooLarge = errors.New("file too large")
	ErrInvalidHash  = errors.New("invalid hash")
	DefaultBucket   = "sandcrawler" // DefaultBucket for S3
)

// BlobStore slightly wraps I/O around our S3 store with convenience methods.
type BlobStore struct {
	Client *minio.Client
}

// BlobStoreOptions mostly contains pass through options for minio client.
// Keys from environment, e.g. ...BLOB_ACCESS_KEY
type BlobStoreOptions struct {
	AccessKey     string
	SecretKey     string
	DefaultBucket string
	UseSSL        bool
}

// NewBlobStore creates a new, slim wrapper around S3.
func NewBlobStore(endpoint string, opts *BlobStoreOptions) (*BlobStore, error) {
	client, err := minio.New(endpoint,
		&minio.Options{
			// Note: seaweedfs (version 8000GB 1.79 linux amd64) may not work
			// with V4!
			Creds:  credentials.NewStaticV2(opts.AccessKey, opts.SecretKey, ""),
			Secure: opts.UseSSL,
		},
	)
	if err != nil {
		return nil, err
	}
	// Quick, additional sanity check if we can connect to S3.
	buckets, err := client.ListBuckets(context.Background())
	if err != nil {
		return nil, fmt.Errorf("could not list S3 buckets: %w", err)
	}
	slog.Info("S3 client ok", "num_buckets", len(buckets))
	for _, bucket := range buckets {
		slog.Debug("found bucket", "bucket", bucket.Name)
	}
	return &BlobStore{
		Client: client,
	}, nil
}

// BlobRequestOptions wraps the blob request options, both for setting and
// retrieving a blob.
//
// Currently used folder names:
//
// - "pdf" for thumbnails
// - "xml_doc" for TEI-XML
// - "html_body" for HTML TEI-XML
// - "unknown" for generic
//
// Default bucket is "sandcrawler-dev", other buckets via infra:
//
// - "sandcrawler" for sandcrawler_grobid_bucket
// - "sandcrawler" for sandcrawler_text_bucket
// - "thumbnail" for sandcrawler_thumbnail_bucket
type BlobRequestOptions struct {
	Folder  string
	Blob    []byte
	SHA1Hex string
	Ext     string
	Prefix  string
	Bucket  string
}

// PutBlobResponse wraps a blob put request response.
type PutBlobResponse struct {
	Bucket     string
	ObjectPath string
}

// blobPath returns the path for a given folder, content hash, extension and
// prefix. Panics if sha1hex is not a length 40 string.
func blobPath(folder, sha1hex, ext, prefix string) string {
	if len(ext) > 0 && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return fmt.Sprintf("%s%s/%s/%s/%s%s",
		prefix, folder, sha1hex[0:2], sha1hex[2:4], sha1hex, ext)
}

// PutBlob puts data in to S3 with key derived from the given options. If the
// options do not contain the SHA1 of the content, it gets computed here.  If
// no bucket name is given, a default bucket name is used. If the bucket does
// not exist, if gets created.
func (bs *BlobStore) PutBlob(ctx context.Context, req *BlobRequestOptions) (*PutBlobResponse, error) {
	if req.SHA1Hex == "" {
		h := sha1.New()
		_, err := io.Copy(h, bytes.NewReader(req.Blob))
		if err != nil {
			return nil, err
		}
		req.SHA1Hex = fmt.Sprintf("%x", h.Sum(nil))
	}
	if len(req.SHA1Hex) != ExpectedSHA1Length {
		return nil, ErrInvalidHash
	}
	objPath := blobPath(req.Folder, req.SHA1Hex, req.Ext, req.Prefix)
	if req.Bucket == "" {
		req.Bucket = DefaultBucket
	}
	ok, err := bs.Client.BucketExists(ctx, req.Bucket)
	if err != nil {
		slog.Error("bucket exist failed", "err", err)
		return nil, err
	}
	if !ok {
		opts := minio.MakeBucketOptions{}
		if err := bs.Client.MakeBucket(ctx, req.Bucket, opts); err != nil {
			slog.Error("make bucket failed", "err", err)
			return nil, err
		}
	}
	// Use mimetype detection for more accurate content type detection
	mtype := mimetype.Detect(req.Blob)
	contentType := mtype.String()
	if contentType == "" {
		// Fallback to extension-based detection if mimetype detection fails
		contentType = "application/octet-stream"
		if strings.HasSuffix(req.Ext, ".xml") {
			contentType = "application/xml"
		}
		if strings.HasSuffix(req.Ext, ".png") {
			contentType = "image/png"
		}
		if strings.HasSuffix(req.Ext, ".jpg") || strings.HasSuffix(req.Ext, ".jpeg") {
			contentType = "image/jpeg"
		}
		if strings.HasSuffix(req.Ext, ".txt") {
			contentType = "text/plain"
		}
	}
	opts := minio.PutObjectOptions{
		ContentType: contentType,
	}
	info, err := bs.Client.PutObject(ctx, req.Bucket, objPath,
		bytes.NewReader(req.Blob), int64(len(req.Blob)), opts)
	if err != nil {
		slog.Error("put object failed", "err", err)
		return nil, err
	}
	if info.Bucket != req.Bucket {
		return nil, fmt.Errorf("[put] bucket mismatch: %v", info.Bucket)
	}
	if info.Key != objPath {
		return nil, fmt.Errorf("[put] key mismatch: %v", info.Key)
	}
	return &PutBlobResponse{
		Bucket:     info.Bucket,
		ObjectPath: info.Key,
	}, nil
}

// GetBlob returns the object bytes given a blob request.
func (bs *BlobStore) GetBlob(ctx context.Context, req *BlobRequestOptions) ([]byte, error) {
	objPath := blobPath(req.Folder, req.SHA1Hex, req.Ext, req.Prefix)
	if req.Bucket == "" {
		req.Bucket = DefaultBucket
	}
	object, err := bs.Client.GetObject(ctx, req.Bucket, objPath, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return io.ReadAll(object)
}
