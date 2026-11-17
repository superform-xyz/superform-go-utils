// Package fileloader loads text files from the local filesystem or Amazon S3.
package fileloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

// S3ObjectGetter is the S3 operation needed to load a file.
type S3ObjectGetter interface {
	GetObjectWithContext(ctx aws.Context, input *s3.GetObjectInput, opts ...request.Option) (*s3.GetObjectOutput, error)
}

// Config configures how a loader creates S3 clients.
type Config struct {
	// NewS3Client can supply application-specific credentials, sessions or endpoints.
	// When nil, the loader uses the AWS SDK's default session configuration.
	NewS3Client func(region string) (S3ObjectGetter, error)
}

// Loader reads local files and S3 objects.
type Loader struct {
	newS3Client func(region string) (S3ObjectGetter, error)
}

// New creates a loader. AWS sessions are created only when loading S3 objects.
func New(cfg Config) *Loader {
	newClient := cfg.NewS3Client
	if newClient == nil {
		newClient = func(region string) (S3ObjectGetter, error) {
			sess, err := session.NewSession(&aws.Config{Region: aws.String(region)})
			if err != nil {
				return nil, fmt.Errorf("failed to create AWS session: %w", err)
			}
			return s3.New(sess), nil
		}
	}
	return &Loader{newS3Client: newClient}
}

// ParseS3URI returns the bucket and key in an s3://bucket/key URI.
func ParseS3URI(s3URI string) (string, string, error) {
	if !strings.HasPrefix(s3URI, "s3://") {
		return "", "", errors.New("invalid S3 URI: must start with 's3://'")
	}
	parsed, err := url.Parse(s3URI)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse S3 URI: %w", err)
	}
	bucket := parsed.Host
	key := strings.TrimPrefix(parsed.Path, "/")
	if bucket == "" || key == "" {
		return "", "", errors.New("invalid S3 URI: must include bucket and key")
	}
	return bucket, key, nil
}

// DownloadFileFromS3 reads an object specified as s3://bucket/key|region.
func (l *Loader) DownloadFileFromS3(ctx context.Context, path string) (string, error) {
	parts := strings.Split(path, "|")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid path: %s should be in the format path|region", path)
	}
	if parts[1] == "" {
		return "", errors.New("region is required")
	}
	client, err := l.newS3Client(parts[1])
	if err != nil {
		return "", fmt.Errorf("failed to create S3 client: %w", err)
	}
	bucket, key, err := ParseS3URI(parts[0])
	if err != nil {
		return "", fmt.Errorf("failed to parse S3 URI: %w", err)
	}
	output, err := client.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", fmt.Errorf("failed to get object %s from bucket %s: %w", key, bucket, err)
	}
	defer output.Body.Close()

	content, err := io.ReadAll(output.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read object body: %w", err)
	}
	return string(content), nil
}

// LoadFile reads a local path or an s3://bucket/key|region object.
func (l *Loader) LoadFile(ctx context.Context, path string) (string, error) {
	if strings.HasPrefix(path, "s3://") {
		return l.DownloadFileFromS3(ctx, path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	return string(content), nil
}
