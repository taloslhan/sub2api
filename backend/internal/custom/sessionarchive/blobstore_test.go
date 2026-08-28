package sessionarchive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"
)

type deadlineS3API struct{}

func (*deadlineS3API) PutObject(ctx context.Context, _ *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*deadlineS3API) GetObject(ctx context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return &s3.GetObjectOutput{Body: &contextReadCloser{ctx: ctx}}, nil
}

func (*deadlineS3API) DeleteObject(ctx context.Context, _ *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*deadlineS3API) ListObjectsV2(ctx context.Context, _ *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type contextReadCloser struct {
	ctx context.Context
}

func (r *contextReadCloser) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func (*contextReadCloser) Close() error { return nil }

func TestS3BlobStoreOperationsHaveBoundedTimeouts(t *testing.T) {
	store, err := newS3BlobStore(&deadlineS3API{}, "private", "archive")
	require.NoError(t, err)
	store.putTimeout = 25 * time.Millisecond
	store.getTimeout = 25 * time.Millisecond
	store.deleteTimeout = 25 * time.Millisecond
	store.listTimeout = 25 * time.Millisecond

	startedAt := time.Now()
	err = store.Put(context.Background(), "archive/blob", bytes.NewReader([]byte("x")), 1)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(startedAt), time.Second)

	reader, err := store.Get(context.Background(), "archive/blob")
	require.NoError(t, err)
	_, err = reader.Read(make([]byte, 1))
	require.ErrorIs(t, err, context.DeadlineExceeded, "Get deadline must cover response body reads")
	require.NoError(t, reader.Close())

	err = store.Delete(context.Background(), "archive/blob")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = store.List(context.Background(), "", "", 10)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestS3BlobStoreRespectsShorterParentDeadline(t *testing.T) {
	store, err := newS3BlobStore(&deadlineS3API{}, "private", "archive")
	require.NoError(t, err)
	store.putTimeout = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	startedAt := time.Now()

	err = store.Put(ctx, "archive/blob", bytes.NewReader([]byte("x")), 1)

	require.True(t, errors.Is(err, context.DeadlineExceeded))
	require.Less(t, time.Since(startedAt), 500*time.Millisecond)
}

var _ io.ReadCloser = (*deadlineReadCloser)(nil)

type pagingS3API struct {
	deadlineS3API
	mu     sync.Mutex
	inputs []*s3.ListObjectsV2Input
}

func (p *pagingS3API) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	p.mu.Lock()
	p.inputs = append(p.inputs, input)
	p.mu.Unlock()
	first := "archive/v1/a.sar"
	second := "archive/v1/b.sar"
	if input.ContinuationToken == nil {
		next := "opaque-next"
		return &s3.ListObjectsV2Output{Contents: []types.Object{{Key: &first}}, NextContinuationToken: &next}, nil
	}
	return &s3.ListObjectsV2Output{Contents: []types.Object{{Key: &second}}}, nil
}

func TestS3BlobStorePreservesOpaquePaginationCursor(t *testing.T) {
	api := &pagingS3API{}
	store, err := newS3BlobStore(api, "private", "archive")
	require.NoError(t, err)
	first, err := store.List(context.Background(), "archive/v1/", "", 1)
	require.NoError(t, err)
	require.Equal(t, []string{"archive/v1/a.sar"}, first.Keys)
	require.Equal(t, "opaque-next", first.NextCursor)
	second, err := store.List(context.Background(), "archive/v1/", first.NextCursor, 1)
	require.NoError(t, err)
	require.Equal(t, []string{"archive/v1/b.sar"}, second.Keys)
	require.Empty(t, second.NextCursor)
	require.Equal(t, "opaque-next", *api.inputs[1].ContinuationToken)
}
