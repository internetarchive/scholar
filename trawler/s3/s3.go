// s3 implements basic functions for interacting with s3 compatible storage
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/viper"
)

// newClient builds a minio client from viper config and splits an s3key of the
// form "bucket/key" into its parts. Every exported function here starts this
// way, so it lives in one place rather than four.
func newClient(s3key string) (*minio.Client, string, string, error) {
	sp := strings.SplitN(s3key, "/", 2)
	if len(sp) != 2 {
		return nil, "", "", errors.New("could not parse s3 key " + s3key)
	}

	// useSSL := true
	useSSL := false // thonk
	mc, err := minio.New(viper.GetString("s3.endpoint"), &minio.Options{
		Creds: credentials.NewStaticV4(
			viper.GetString("s3.access_id"),
			viper.GetString("s3.secret_key"),
			"",
		),
		Secure: useSSL,
	})
	if err != nil {
		return nil, "", "", fmt.Errorf("minio client init failed: %w", err)
	}

	return mc, sp[0], sp[1], nil
}

func GetObject(ctx context.Context, s3key string) (*minio.Object, error) {
	mc, bucket, key, err := newClient(s3key)
	if err != nil {
		return nil, err
	}

	f, err := mc.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("GetObject failed: %w", err)
	}
	return f, nil
}

func PutObject(ctx context.Context, s3key string, body []byte, contentType string) error {
	mc, bucket, key, err := newClient(s3key)
	if err != nil {
		return err
	}

	opts := minio.PutObjectOptions{ContentType: contentType}
	_, err = mc.PutObject(ctx, bucket, key, bytes.NewReader(body), int64(len(body)), opts)
	if err != nil {
		return fmt.Errorf("PutObject failed: %w", err)
	}
	return nil
}

// ObjectExists reports whether s3key is present. A missing object is not an
// error -- the question this answers is "has this already been written", and
// "no" is a perfectly good answer.
//
// It exists so work that stages a derived object in S3 can skip re-deriving it.
// Note that presence says nothing about size: a legitimately empty object (an
// item whose CDX holds no PDFs, say) is cached just like any other, and
// treating it as absent would re-derive it on every pass.
func ObjectExists(ctx context.Context, s3key string) (bool, error) {
	mc, bucket, key, err := newClient(s3key)
	if err != nil {
		return false, err
	}

	if _, err := mc.StatObject(ctx, bucket, key, minio.StatObjectOptions{}); err != nil {
		resp := minio.ToErrorResponse(err)
		if resp.StatusCode == http.StatusNotFound || resp.Code == "NoSuchKey" {
			return false, nil
		}
		return false, fmt.Errorf("StatObject failed: %w", err)
	}
	return true, nil
}

// RemoveObject is reserved for future one-off PDF ingestion tooling alongside
// PutObject. The periodic-ingest workflow does not stage anything in
// SeaweedFS, so there is nothing to remove from this pipeline.
func RemoveObject(ctx context.Context, s3key string) error {
	mc, bucket, key, err := newClient(s3key)
	if err != nil {
		return err
	}

	if err := mc.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("RemoveObject failed: %w", err)
	}
	return nil
}
