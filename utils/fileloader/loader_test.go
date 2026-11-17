package fileloader

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/stretchr/testify/require"
)

type s3GetterFunc func(aws.Context, *s3.GetObjectInput, ...request.Option) (*s3.GetObjectOutput, error)

func (f s3GetterFunc) GetObjectWithContext(ctx aws.Context, input *s3.GetObjectInput, opts ...request.Option) (*s3.GetObjectOutput, error) {
	return f(ctx, input, opts...)
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func TestParseS3URI(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		uri, bucket, key, err string
	}{
		{uri: "s3://bucket/path/to/file.json", bucket: "bucket", key: "path/to/file.json"},
		{uri: "s3://bucket/file.json", bucket: "bucket", key: "file.json"},
		{uri: "https://bucket/file.json", err: "must start with 's3://'"},
		{uri: "s3://bucket/", err: "must include bucket and key"},
		{uri: "s3:///file.json", err: "must include bucket and key"},
		{uri: "s3://bucket/%zz", err: "failed to parse S3 URI"},
	} {
		t.Run(tt.uri, func(t *testing.T) {
			bucket, key, err := ParseS3URI(tt.uri)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.bucket, bucket)
			require.Equal(t, tt.key, key)
		})
	}
}

func TestLoadFileLocal(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"key":"value"}`), 0o600))
	loader := New(Config{})
	content, err := loader.LoadFile(t.Context(), path)
	require.NoError(t, err)
	require.Equal(t, `{"key":"value"}`, content)

	_, err = loader.LoadFile(t.Context(), path+".missing")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestLoadFileS3(t *testing.T) {
	t.Parallel()
	body := &trackedBody{Reader: strings.NewReader("s3 content")}
	ctx := t.Context()
	loader := New(Config{NewS3Client: func(region string) (S3ObjectGetter, error) {
		require.Equal(t, "eu-central-1", region)
		return s3GetterFunc(func(gotCtx aws.Context, input *s3.GetObjectInput, _ ...request.Option) (*s3.GetObjectOutput, error) {
			require.Equal(t, ctx, gotCtx)
			require.Equal(t, "bucket", aws.StringValue(input.Bucket))
			require.Equal(t, "path/config.json", aws.StringValue(input.Key))
			return &s3.GetObjectOutput{Body: body}, nil
		}), nil
	}})
	content, err := loader.LoadFile(ctx, "s3://bucket/path/config.json|eu-central-1")
	require.NoError(t, err)
	require.Equal(t, "s3 content", content)
	require.True(t, body.closed)
}

func TestDownloadFileFromS3InvalidPath(t *testing.T) {
	t.Parallel()
	loader := New(Config{NewS3Client: func(string) (S3ObjectGetter, error) { return nil, nil }})
	for _, tt := range []struct{ path, err string }{
		{path: "s3://bucket/key", err: "invalid path"},
		{path: "s3://bucket/key|", err: "region is required"},
		{path: "s3://bucket/key|us-east-1|extra", err: "invalid path"},
		{path: "not-s3://bucket/key|us-east-1", err: "failed to parse S3 URI"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			_, err := loader.DownloadFileFromS3(t.Context(), tt.path)
			require.ErrorContains(t, err, tt.err)
		})
	}
}

func TestDownloadFileFromS3FactoryError(t *testing.T) {
	t.Parallel()
	want := errors.New("session creation failed")
	loader := New(Config{NewS3Client: func(string) (S3ObjectGetter, error) { return nil, want }})
	_, err := loader.DownloadFileFromS3(t.Context(), "s3://bucket/key|us-east-1")
	require.ErrorIs(t, err, want)
}

func TestDownloadFileFromS3RequestError(t *testing.T) {
	t.Parallel()
	want := errors.New("access denied")
	loader := New(Config{NewS3Client: func(string) (S3ObjectGetter, error) {
		return s3GetterFunc(func(aws.Context, *s3.GetObjectInput, ...request.Option) (*s3.GetObjectOutput, error) {
			return nil, want
		}), nil
	}})
	_, err := loader.DownloadFileFromS3(t.Context(), "s3://bucket/key|us-east-1")
	require.ErrorIs(t, err, want)
}

func TestDownloadFileFromS3ReadErrorClosesBody(t *testing.T) {
	t.Parallel()
	want := errors.New("read failed")
	body := &trackedBody{Reader: errorReader{err: want}}
	loader := New(Config{NewS3Client: func(string) (S3ObjectGetter, error) {
		return s3GetterFunc(func(aws.Context, *s3.GetObjectInput, ...request.Option) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{Body: body}, nil
		}), nil
	}})
	_, err := loader.DownloadFileFromS3(t.Context(), "s3://bucket/key|us-east-1")
	require.ErrorIs(t, err, want)
	require.True(t, body.closed)
}

func TestDownloadFileFromS3Cancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	loader := New(Config{NewS3Client: func(string) (S3ObjectGetter, error) {
		return s3GetterFunc(func(gotCtx aws.Context, _ *s3.GetObjectInput, _ ...request.Option) (*s3.GetObjectOutput, error) {
			require.Equal(t, ctx, gotCtx)
			return nil, gotCtx.Err()
		}), nil
	}})
	_, err := loader.DownloadFileFromS3(ctx, "s3://bucket/key|us-east-1")
	require.ErrorIs(t, err, context.Canceled)
}
