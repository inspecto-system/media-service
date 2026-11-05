// Package objectstore keeps photo files in the S3-compatible storage (SeaweedFS, ADR-024) through
// the standard AWS SDK, so the storage product can be replaced without code changes.
package objectstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/inspecto-system/media-service/internal/config"
)

// ErrNotFound means there is no object with the key.
var ErrNotFound = errors.New("object not found")

const contentType = "image/jpeg"

// Store reads and writes objects of one bucket. The bucket is private; clients never get direct
// links, every file goes through media-service (SECURITY.md §7).
type Store struct {
	client *s3.Client
	bucket string
}

// New returns a store for the configured bucket.
func New(cfg config.Storage) *Store {
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(cfg.Endpoint),
		Region:       cfg.Region,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		UsePathStyle: true,
		// Checksums only where S3 requires them; SeaweedFS does not need the newer default ones.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Store{client: client, bucket: cfg.Bucket}
}

// Put stores a JPEG under the key.
func (s *Store) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}

// Get opens the object; the caller closes the reader.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		var missing *types.NoSuchKey
		if errors.As(err, &missing) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, fmt.Errorf("get object: %w", err)
	}
	return out.Body, aws.ToInt64(out.ContentLength), nil
}

// Delete removes the objects; missing objects are not an error, so a retried delete succeeds.
func (s *Store) Delete(ctx context.Context, keys ...string) error {
	var errs []error
	for _, key := range keys {
		_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
		if err != nil {
			errs = append(errs, fmt.Errorf("delete object %s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

// Ping checks that the bucket is reachable, for the readiness probe.
func (s *Store) Ping(ctx context.Context) error {
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return fmt.Errorf("object storage: %w", err)
	}
	return nil
}
