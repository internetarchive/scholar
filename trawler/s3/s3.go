// s3 implements basic functions for interacting with s3 compatible storage
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/viper"
)

func GetObject(ctx context.Context, s3key string) (*minio.Object, error) {
	endpoint := viper.GetString("s3.endpoint")
	accessKeyID := viper.GetString("s3.access_id")
	secretAccessKey := viper.GetString("s3.secret_key")
	// useSSL := true
	useSSL := false // thonk
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})
	sp := strings.SplitN(s3key, "/", 2)
	if len(sp) != 2 {
		return nil, errors.New("could not parse s3 key " + s3key)
	}
	bucket := sp[0]
	key := sp[1]

	opts := minio.GetObjectOptions{}
	f, err := mc.GetObject(ctx, bucket, key, opts)
	if err != nil {
		return nil, fmt.Errorf("GetObject failed: %w", err)
	}
	return f, nil
}

// PutObject is reserved for future one-off PDF ingestion tooling. The
// periodic-ingest workflow reads PDF bytes directly from petabox; nothing
// in the periodic pipeline calls PutObject today. See periodic/upload.go.
func PutObject(ctx context.Context, s3key string, body []byte, contentType string) error {
	endpoint := viper.GetString("s3.endpoint")
	accessKeyID := viper.GetString("s3.access_id")
	secretAccessKey := viper.GetString("s3.secret_key")
	useSSL := false
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return fmt.Errorf("minio client init failed: %w", err)
	}

	sp := strings.SplitN(s3key, "/", 2)
	if len(sp) != 2 {
		return errors.New("could not parse s3 key " + s3key)
	}
	bucket := sp[0]
	key := sp[1]

	opts := minio.PutObjectOptions{ContentType: contentType}
	_, err = mc.PutObject(ctx, bucket, key, bytes.NewReader(body), int64(len(body)), opts)
	if err != nil {
		return fmt.Errorf("PutObject failed: %w", err)
	}
	return nil
}

// RemoveObject is reserved for future one-off PDF ingestion tooling alongside
// PutObject. The periodic-ingest workflow does not stage anything in
// SeaweedFS, so there is nothing to remove from this pipeline.
func RemoveObject(ctx context.Context, s3key string) error {
	endpoint := viper.GetString("s3.endpoint")
	accessKeyID := viper.GetString("s3.access_id")
	secretAccessKey := viper.GetString("s3.secret_key")
	useSSL := false
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return fmt.Errorf("minio client init failed: %w", err)
	}

	sp := strings.SplitN(s3key, "/", 2)
	if len(sp) != 2 {
		return errors.New("could not parse s3 key " + s3key)
	}
	bucket := sp[0]
	key := sp[1]

	err = mc.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("RemoveObject failed: %w", err)
	}
	return nil
}
