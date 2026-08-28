package sessionarchive

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type hangingSelfCheckStore struct {
	release chan struct{}
	entered chan struct{}
}

type recordingBlobStore struct {
	selfChecks int
	listCalls  []string
	deletes    []string
}

func (*recordingBlobStore) Put(context.Context, string, io.Reader, int64) error { return nil }
func (*recordingBlobStore) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (s *recordingBlobStore) Delete(_ context.Context, key string) error {
	s.deletes = append(s.deletes, key)
	return nil
}
func (s *recordingBlobStore) List(_ context.Context, prefix, cursor string, _ int32) (BlobListPage, error) {
	s.listCalls = append(s.listCalls, prefix+"|"+cursor)
	if cursor == "" {
		return BlobListPage{Keys: []string{"cas/v1/orphan-a.sar", "cas/v1/kept.sar"}, NextCursor: "page-2"}, nil
	}
	return BlobListPage{Keys: []string{"cas/v1/orphan-b.sar"}}, nil
}
func (s *recordingBlobStore) SelfCheck(context.Context) error {
	s.selfChecks++
	return nil
}

func testServiceConfig(backend string) config.SessionArchiveConfig {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	return config.SessionArchiveConfig{
		Enabled: true, StorageBackend: backend,
		WorkerCount: 1, QueueSize: 1, QueueMaxBytes: 1024, PayloadMaxBytes: 512,
		ShutdownDrainSeconds: 1, DefaultRetentionDays: 1, MergeWindowSeconds: 1,
		MaintenanceIntervalSecs: 60, CleanupBatchSize: 2, GCGraceSeconds: 0,
		PostgreSQL:  config.SessionArchivePostgreSQLConfig{ChunkSizeBytes: 1024 * 1024},
		ActiveKeyID: "v1", EncryptionKeys: map[string]string{"v1": key},
	}
}

func (*hangingSelfCheckStore) Put(context.Context, string, io.Reader, int64) error { return nil }
func (*hangingSelfCheckStore) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (*hangingSelfCheckStore) Delete(context.Context, string) error { return nil }
func (*hangingSelfCheckStore) List(context.Context, string, string, int32) (BlobListPage, error) {
	return BlobListPage{}, nil
}
func (s *hangingSelfCheckStore) SelfCheck(context.Context) error {
	close(s.entered)
	<-s.release
	return nil
}

func TestAsyncStartCannotPublishAfterShutdownCancellation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(`(?s)SELECT.*to_regclass\('session_archive_sessions'\)`).
		WillReturnRows(sqlmock.NewRows([]string{"sessions", "blobs", "objects", "chunks", "column", "cas", "object"}).AddRow(true, true, true, true, true, true, true))
	mock.ExpectQuery(`(?s)SELECT EXISTS.*stored_plaintext_sha256`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`SELECT DISTINCT storage_backend`).
		WillReturnRows(sqlmock.NewRows([]string{"storage_backend"}))

	cfg := testServiceConfig(StorageBackendFilesystem)
	store := &hangingSelfCheckStore{release: make(chan struct{}), entered: make(chan struct{})}
	service, err := NewService(context.Background(), ServiceOptions{Config: cfg, DB: db, BlobStore: store, TempDir: t.TempDir(), DataDir: t.TempDir()})
	require.NoError(t, err)
	reported := make(chan error, 1)
	service.StartAsync(func(err error) { reported <- err })
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("startup did not enter storage self-check")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, service.Shutdown(shutdownCtx), context.DeadlineExceeded)
	close(store.release)
	require.Eventually(t, func() bool { return !service.started.Load() }, time.Second, 10*time.Millisecond)
	select {
	case err := <-reported:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("late startup did not report cancellation")
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStartRejectsNonS3BackendUntilLegacyConstraintsAreFinalized(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(`(?s)SELECT.*to_regclass\('session_archive_sessions'\)`).
		WillReturnRows(sqlmock.NewRows([]string{"sessions", "blobs", "objects", "chunks", "column", "cas", "object"}).AddRow(true, true, true, true, true, true, true))
	mock.ExpectQuery(`(?s)SELECT EXISTS.*stored_plaintext_sha256`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	store := &recordingBlobStore{}
	service, err := NewService(context.Background(), ServiceOptions{Config: testServiceConfig(StorageBackendFilesystem), DB: db, BlobStore: store, TempDir: t.TempDir(), DataDir: t.TempDir()})
	require.NoError(t, err)
	defer service.cancel()

	err = service.Start(context.Background())

	require.ErrorContains(t, err, "waiting for migration 237")
	require.Zero(t, store.selfChecks, "the storage backend must not be touched before the schema gate passes")
	require.False(t, service.started.Load())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBuildStoreRegistryDoesNotConstructUnconfiguredInactiveS3(t *testing.T) {
	cfg := testServiceConfig(StorageBackendFilesystem)
	cfg.Filesystem.Root = t.TempDir()
	cfg.S3 = config.SessionArchiveS3Config{}
	service := &Service{cfg: cfg, dataDir: t.TempDir()}

	registry, err := service.buildStoreRegistry(context.Background(), []string{StorageBackendFilesystem})

	require.NoError(t, err)
	defer func() { _ = registry.Close() }()
	entries := registry.Entries()
	require.Len(t, entries, 1)
	require.Equal(t, StorageBackendFilesystem, entries[0].Backend)
}

func TestPhysicalOrphanReconciliationPaginatesOnlyCASNamespace(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	store := &recordingBlobStore{}
	registry, err := NewStoreRegistry(StorageBackendFilesystem, StoreEntry{
		Backend: StorageBackendFilesystem, Store: store, Namespace: "cas", Ready: true,
	})
	require.NoError(t, err)
	service := &Service{
		cfg: config.SessionArchiveConfig{CleanupBatchSize: 2}, repository: repository,
		metrics: &serviceMetrics{}, reconcileCursors: make(map[string]string),
	}
	for _, result := range []bool{false, true, false} {
		mock.ExpectQuery(`SELECT EXISTS.*storage_backend=\$1 AND object_key=\$2`).
			WithArgs(StorageBackendFilesystem, sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(result))
	}

	service.reconcilePhysicalOrphans(context.Background(), registry)
	service.reconcilePhysicalOrphans(context.Background(), registry)

	require.Equal(t, []string{"cas/v1/|", "cas/v1/|page-2"}, store.listCalls)
	require.Equal(t, []string{"cas/v1/orphan-a.sar", "cas/v1/orphan-b.sar"}, store.deletes)
	for _, key := range append(append([]string{}, store.listCalls...), store.deletes...) {
		require.NotContains(t, key, "self-check")
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPhysicalOrphanReconciliationRetriesPageAfterDatabaseLookupFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	store := &recordingBlobStore{}
	registry, err := NewStoreRegistry(StorageBackendFilesystem, StoreEntry{
		Backend: StorageBackendFilesystem, Store: store, Namespace: "cas", Ready: true,
	})
	require.NoError(t, err)
	service := &Service{
		cfg: config.SessionArchiveConfig{CleanupBatchSize: 2}, repository: repository,
		metrics: &serviceMetrics{}, reconcileCursors: make(map[string]string),
	}
	mock.ExpectQuery(`SELECT EXISTS.*storage_backend=\$1 AND object_key=\$2`).
		WithArgs(StorageBackendFilesystem, "cas/v1/orphan-a.sar").
		WillReturnError(errors.New("database unavailable"))

	service.reconcilePhysicalOrphans(context.Background(), registry)

	require.Equal(t, []string{"cas/v1/|"}, store.listCalls)
	require.Empty(t, store.deletes)
	require.Empty(t, service.reconcileCursors, "a partial page must be retried instead of advancing its cursor")
	require.NoError(t, mock.ExpectationsWereMet())
}
