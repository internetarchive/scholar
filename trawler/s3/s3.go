// s3 implements basic functions for interacting with s3 compatible storage
package s3

import (
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
