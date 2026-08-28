package sessionarchive

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type registryTestStore struct{}

func (*registryTestStore) Put(context.Context, string, io.Reader, int64) error { return nil }
func (*registryTestStore) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (*registryTestStore) Delete(context.Context, string) error { return nil }
func (*registryTestStore) List(context.Context, string, string, int32) (BlobListPage, error) {
	return BlobListPage{}, nil
}
func (*registryTestStore) SelfCheck(context.Context) error { return nil }

func TestStoreRegistryNeverFallsBackAcrossBackends(t *testing.T) {
	registry, err := NewStoreRegistry(StorageBackendFilesystem,
		StoreEntry{Backend: StorageBackendFilesystem, Store: &registryTestStore{}, Namespace: "cas"},
		StoreEntry{Backend: StorageBackendS3, Store: &registryTestStore{}, Namespace: "archive"},
	)
	require.NoError(t, err)
	registry.SetHealth(StorageBackendFilesystem, true, nil)
	registry.SetHealth(StorageBackendS3, false, errors.New("credentials missing"))

	active, err := registry.Active()
	require.NoError(t, err)
	require.Equal(t, StorageBackendFilesystem, active.Backend)
	_, err = registry.Resolve(StorageBackendS3)
	require.ErrorIs(t, err, ErrStorageBackendUnavailable)
	_, err = registry.Resolve(StorageBackendPostgreSQL)
	require.ErrorIs(t, err, ErrStorageBackendUnavailable)
	require.Equal(t, []string{StorageBackendFilesystem}, registry.ReadyBackends())
}
