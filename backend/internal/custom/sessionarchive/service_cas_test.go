package sessionarchive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeBlobReservationRepository struct {
	mu           sync.Mutex
	status       string
	ownerToken   string
	leaseExpires time.Time
	reserveCalls int
}

func (r *fakeBlobReservationRepository) ReserveBlob(_ context.Context, info EncodingInfo, objectKey, ownerToken string, lease time.Duration) (BlobRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reserveCalls++
	now := time.Now()
	if r.status == "" {
		r.status, r.ownerToken, r.leaseExpires = "pending", ownerToken, now.Add(lease)
		return BlobRecord{ID: 1, Info: info, ObjectKey: objectKey, Status: r.status}, true, nil
	}
	owner := false
	if r.status == "failed" || r.status == "gc_pending" || (r.status == "pending" && !now.Before(r.leaseExpires)) {
		r.status, r.ownerToken, r.leaseExpires = "pending", ownerToken, now.Add(lease)
		owner = true
	}
	return BlobRecord{ID: 1, Info: info, ObjectKey: objectKey, Status: r.status}, owner, nil
}

func (r *fakeBlobReservationRepository) MarkBlobReady(_ context.Context, _ int64, ownerToken string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status != "pending" || r.ownerToken != ownerToken {
		return errors.New("blob upload ownership lost")
	}
	r.status, r.ownerToken = "ready", ""
	return nil
}

func (r *fakeBlobReservationRepository) MarkBlobFailed(_ context.Context, _ int64, ownerToken string, _ error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == "pending" && r.ownerToken == ownerToken {
		r.status, r.ownerToken = "failed", ""
	}
	return nil
}

type slowBlobStore struct {
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	putCount  atomic.Int32
}

func (s *slowBlobStore) Put(ctx context.Context, _ string, body io.Reader, _ int64) error {
	s.putCount.Add(1)
	s.startOnce.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
	}
	_, err := io.Copy(io.Discard, body)
	return err
}

func (*slowBlobStore) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (*slowBlobStore) Delete(context.Context, string) error                  { return nil }
func (*slowBlobStore) List(context.Context, string, int32) ([]string, error) { return nil, nil }
func (*slowBlobStore) SelfCheck(context.Context) error                       { return nil }

func TestStoreCASBlobWaitsForSlowOwnerAndReusesReadyBlob(t *testing.T) {
	repository := &fakeBlobReservationRepository{}
	store := &slowBlobStore{started: make(chan struct{}), release: make(chan struct{})}
	payload := []byte("encrypted archive payload")
	info := EncodingInfo{StoredPlaintextSHA256: "hash", FormatVersion: 1, KeyID: "key-1", CiphertextBytes: int64(len(payload))}
	type result struct {
		blob BlobRecord
		err  error
	}
	ownerResult := make(chan result, 1)
	go func() {
		blob, err := storeCASBlob(context.Background(), repository, store, bytes.NewReader(payload), info, "archive/key", 500*time.Millisecond, time.Second)
		ownerResult <- result{blob: blob, err: err}
	}()
	<-store.started

	contenderResult := make(chan result, 1)
	go func() {
		blob, err := storeCASBlob(context.Background(), repository, store, bytes.NewReader(payload), info, "archive/key", 500*time.Millisecond, time.Second)
		contenderResult <- result{blob: blob, err: err}
	}()
	select {
	case <-contenderResult:
		t.Fatal("contender returned before the owning upload became ready")
	case <-time.After(40 * time.Millisecond):
	}
	close(store.release)

	owner := <-ownerResult
	contender := <-contenderResult
	require.NoError(t, owner.err)
	require.NoError(t, contender.err)
	require.Equal(t, owner.blob.ID, contender.blob.ID)
	require.Equal(t, "ready", contender.blob.Status)
	require.Equal(t, int32(1), store.putCount.Load(), "the contender must reuse the owner's CAS object")
	repository.mu.Lock()
	require.GreaterOrEqual(t, repository.reserveCalls, 3)
	require.Equal(t, "ready", repository.status)
	repository.mu.Unlock()
}

func TestStoreCASBlobTakesOverExpiredPendingLease(t *testing.T) {
	repository := &fakeBlobReservationRepository{status: "pending", ownerToken: "dead-owner", leaseExpires: time.Now().Add(-time.Second)}
	store := &slowBlobStore{started: make(chan struct{}), release: make(chan struct{})}
	close(store.release)
	payload := []byte("ciphertext")
	info := EncodingInfo{StoredPlaintextSHA256: "hash", FormatVersion: 1, KeyID: "key-1", CiphertextBytes: int64(len(payload))}

	blob, err := storeCASBlob(context.Background(), repository, store, bytes.NewReader(payload), info, "archive/key", time.Second, time.Second)

	require.NoError(t, err)
	require.Equal(t, "ready", blob.Status)
	require.Equal(t, int32(1), store.putCount.Load())
}

func TestStoreCASBlobTakesOverFailedReservation(t *testing.T) {
	repository := &fakeBlobReservationRepository{status: "failed"}
	store := &slowBlobStore{started: make(chan struct{}), release: make(chan struct{})}
	close(store.release)
	payload := []byte("ciphertext")
	info := EncodingInfo{StoredPlaintextSHA256: "hash", FormatVersion: 1, KeyID: "key-1", CiphertextBytes: int64(len(payload))}

	blob, err := storeCASBlob(context.Background(), repository, store, bytes.NewReader(payload), info, "archive/key", time.Second, time.Second)

	require.NoError(t, err)
	require.Equal(t, "ready", blob.Status)
	require.Equal(t, int32(1), store.putCount.Load())
}

func TestStoreCASBlobPendingWaitIsBounded(t *testing.T) {
	repository := &fakeBlobReservationRepository{status: "pending", ownerToken: "slow-owner", leaseExpires: time.Now().Add(time.Minute)}
	store := &slowBlobStore{started: make(chan struct{}), release: make(chan struct{})}
	startedAt := time.Now()

	_, err := storeCASBlob(context.Background(), repository, store, bytes.NewReader([]byte("ciphertext")), EncodingInfo{}, "archive/key", time.Minute, 30*time.Millisecond)

	require.ErrorContains(t, err, "timed out waiting for pending blob")
	require.Less(t, time.Since(startedAt), 500*time.Millisecond)
	require.Zero(t, store.putCount.Load())
}

func TestStorageFailureEventDropsStoredBodyButKeepsObservation(t *testing.T) {
	event := CaptureEvent{Observation: Observation{
		StoredPayload: []byte("body"), ObservedSHA256: "observed", StoredSHA256: "stored",
		ObservedBytes: 9, StoredBytes: 4, Truncated: true,
	}}

	got := storageFailureEvent(event)

	require.Nil(t, got.Observation.StoredPayload)
	require.Empty(t, got.Observation.StoredSHA256)
	require.Zero(t, got.Observation.StoredBytes)
	require.Equal(t, "observed", got.Observation.ObservedSHA256)
	require.Equal(t, int64(9), got.Observation.ObservedBytes)
	require.True(t, got.Observation.Truncated)
	require.Equal(t, "storage_failed", got.Observation.DroppedReason)
}
