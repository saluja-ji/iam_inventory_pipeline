// Package store persists pipeline outputs under deterministic, date-partitioned keys,
// so re-running on the same day overwrites instead of duplicating.
package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"go.opentelemetry.io/otel"
)

var tracer = otel.Tracer("iam-inventory/store")

// ErrNotFound is returned by Get when the key does not exist.
var ErrNotFound = errors.New("object not found")

// Backend is a minimal object store.
type Backend interface {
	Put(ctx context.Context, key string, body []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
}

func RawKey(dt string) string      { return fmt.Sprintf("raw/dt=%s/iam.json", dt) }
func FindingsKey(dt string) string { return fmt.Sprintf("findings/dt=%s/findings.json", dt) }
func DiffKey(dt string) string     { return fmt.Sprintf("diff/dt=%s/diff.json", dt) }
func CuratedKey(table, dt string) string {
	return fmt.Sprintf("curated/%s/dt=%s/%s.parquet", table, dt, table)
}

// S3API is the subset of the S3 client used here.
type S3API interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

type S3 struct {
	Client S3API
	Bucket string
}

func (s S3) Put(ctx context.Context, key string, body []byte, contentType string) error {
	ctx, span := tracer.Start(ctx, "store.put")
	defer span.End()
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.Bucket), Key: aws.String(key), Body: bytes.NewReader(body),
		ContentType: aws.String(contentType), ServerSideEncryption: types.ServerSideEncryptionAes256,
	})
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("put s3://%s/%s: %w", s.Bucket, key, err)
	}
	return nil
}

func (s S3) Get(ctx context.Context, key string) ([]byte, error) {
	ctx, span := tracer.Start(ctx, "store.get")
	defer span.End()
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, ErrNotFound
		}
		span.RecordError(err)
		return nil, fmt.Errorf("get s3://%s/%s: %w", s.Bucket, key, err)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

// Dir stores objects on the local filesystem; used for offline runs and tests.
type Dir struct{ Root string }

func (d Dir) Put(_ context.Context, key string, body []byte, _ string) error {
	p := filepath.Join(d.Root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	return os.WriteFile(p, body, 0o600)
}

func (d Dir) Get(_ context.Context, key string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(d.Root, filepath.FromSlash(key)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}
