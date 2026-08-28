package sessionarchive

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type BlobStore interface {
	Put(context.Context, string, io.Reader, int64) error
	Get(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
	List(context.Context, string, int32) ([]string, error)
	SelfCheck(context.Context) error
}

type s3API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

type S3BlobStore struct {
	client        s3API
	bucket        string
	prefix        string
	putTimeout    time.Duration
	getTimeout    time.Duration
	deleteTimeout time.Duration
	listTimeout   time.Duration
}

const (
	defaultS3PutTimeout    = 2 * time.Minute
	defaultS3GetTimeout    = 2 * time.Minute
	defaultS3DeleteTimeout = 30 * time.Second
	defaultS3ListTimeout   = 30 * time.Second
)

func NewS3BlobStore(ctx context.Context, params repository.S3ClientParams, bucket, prefix string) (*S3BlobStore, error) {
	client, err := repository.NewS3Client(ctx, params)
	if err != nil {
		return nil, err
	}
	return newS3BlobStore(client, bucket, prefix)
}

func newS3BlobStore(client s3API, bucket, prefix string) (*S3BlobStore, error) {
	bucket = strings.TrimSpace(bucket)
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if client == nil || bucket == "" || prefix == "" || strings.Contains(prefix, "..") {
		return nil, errors.New("invalid private blob store configuration")
	}
	return &S3BlobStore{
		client: client, bucket: bucket, prefix: prefix,
		putTimeout: defaultS3PutTimeout, getTimeout: defaultS3GetTimeout,
		deleteTimeout: defaultS3DeleteTimeout, listTimeout: defaultS3ListTimeout,
	}, nil
}

func (s *S3BlobStore) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	if err := s.validateKey(key); err != nil {
		return err
	}
	if size < 0 {
		return errors.New("invalid blob size")
	}
	opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout(s.putTimeout, defaultS3PutTimeout))
	defer cancel()
	contentType := "application/octet-stream"
	_, err := s.client.PutObject(opCtx, &s3.PutObjectInput{
		Bucket: &s.bucket, Key: &key, Body: body, ContentLength: &size, ContentType: &contentType,
	})
	if err != nil {
		return fmt.Errorf("put private archive object: %w", err)
	}
	return nil
}

func (s *S3BlobStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := s.validateKey(key); err != nil {
		return nil, err
	}
	opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout(s.getTimeout, defaultS3GetTimeout))
	output, err := s.client.GetObject(opCtx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("get private archive object: %w", err)
	}
	if output == nil || output.Body == nil {
		cancel()
		return nil, errors.New("get private archive object returned no body")
	}
	return &deadlineReadCloser{ReadCloser: output.Body, cancel: cancel}, nil
}

func (s *S3BlobStore) Delete(ctx context.Context, key string) error {
	if err := s.validateKey(key); err != nil {
		return err
	}
	opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout(s.deleteTimeout, defaultS3DeleteTimeout))
	defer cancel()
	_, err := s.client.DeleteObject(opCtx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return fmt.Errorf("delete private archive object: %w", err)
	}
	return nil
}

func (s *S3BlobStore) List(ctx context.Context, relativePrefix string, limit int32) ([]string, error) {
	prefix := s.prefix + "/" + strings.TrimLeft(relativePrefix, "/")
	if strings.Contains(relativePrefix, "..") {
		return nil, errors.New("invalid archive list prefix")
	}
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	opCtx, cancel := context.WithTimeout(ctx, s.operationTimeout(s.listTimeout, defaultS3ListTimeout))
	defer cancel()
	output, err := s.client.ListObjectsV2(opCtx, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &prefix, MaxKeys: &limit})
	if err != nil {
		return nil, fmt.Errorf("list private archive objects: %w", err)
	}
	keys := make([]string, 0, len(output.Contents))
	for _, object := range output.Contents {
		if object.Key != nil && strings.HasPrefix(*object.Key, s.prefix+"/") {
			keys = append(keys, *object.Key)
		}
	}
	return keys, nil
}

func (s *S3BlobStore) SelfCheck(ctx context.Context) error {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	key := s.prefix + "/self-check/" + hex.EncodeToString(random)
	payload := []byte("session-archive-private-self-check")
	if err := s.Put(ctx, key, bytes.NewReader(payload), int64(len(payload))); err != nil {
		return err
	}
	reader, err := s.Get(ctx, key)
	if err != nil {
		_ = s.Delete(context.Background(), key)
		return err
	}
	got, readErr := io.ReadAll(io.LimitReader(reader, int64(len(payload)+1)))
	closeErr := reader.Close()
	deleteErr := s.Delete(ctx, key)
	if readErr != nil || closeErr != nil || deleteErr != nil || string(got) != string(payload) {
		return errors.New("private archive object store write/read/delete self-check failed")
	}
	return nil
}

func (s *S3BlobStore) validateKey(key string) error {
	if !strings.HasPrefix(key, s.prefix+"/") || strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		return errors.New("archive object key escapes dedicated prefix")
	}
	return nil
}

func (*S3BlobStore) operationTimeout(configured, fallback time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	return fallback
}

type deadlineReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *deadlineReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}
