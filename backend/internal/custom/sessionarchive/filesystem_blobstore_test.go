package sessionarchive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilesystemBlobStoreContractAndPagination(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "archive")
	store, err := NewFilesystemBlobStore(rootPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	keys := []string{"cas/v1/key/a.sar", "cas/v1/key/b.sar", "cas/v1/key/c.sar"}
	for _, key := range keys {
		payload := []byte(key)
		require.NoError(t, store.Put(context.Background(), key, bytes.NewReader(payload), int64(len(payload))))
		require.NoError(t, store.Put(context.Background(), key, bytes.NewReader(payload), int64(len(payload))), "same-size retry is idempotent")
	}
	reader, err := store.Get(context.Background(), keys[0])
	require.NoError(t, err)
	content, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, []byte(keys[0]), content)

	first, err := store.List(context.Background(), "cas/v1", "", 2)
	require.NoError(t, err)
	require.Len(t, first.Keys, 2)
	require.NotEmpty(t, first.NextCursor)
	second, err := store.List(context.Background(), "cas/v1", first.NextCursor, 2)
	require.NoError(t, err)
	require.Len(t, second.Keys, 1)
	all := append(append([]string(nil), first.Keys...), second.Keys...)
	sort.Strings(all)
	require.Equal(t, keys, all)

	info, err := os.Stat(filepath.Join(rootPath, filepath.FromSlash(keys[0])))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.Error(t, store.Put(context.Background(), keys[0], bytes.NewReader([]byte("different-size")), 14))
	require.NoError(t, store.Delete(context.Background(), keys[0]))
	require.NoError(t, store.Delete(context.Background(), keys[0]))
}

func TestFilesystemBlobStoreRejectsEscapesNonRegularFilesAndBadSizes(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "archive")
	store, err := NewFilesystemBlobStore(rootPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	for _, key := range []string{"", "/absolute", "../outside", "cas/../outside", `cas\\windows`} {
		require.Error(t, store.Put(context.Background(), key, bytes.NewReader(nil), 0), key)
	}
	require.Error(t, store.Put(context.Background(), "cas/short", bytes.NewReader([]byte("x")), 2))
	require.Error(t, store.Put(context.Background(), "cas/long", bytes.NewReader([]byte("xx")), 1))

	require.NoError(t, os.MkdirAll(filepath.Join(rootPath, "cas", "directory"), 0o700))
	_, err = store.Get(context.Background(), "cas/directory")
	require.Error(t, err)

	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(rootPath, "cas", "outside-link")))
	_, err = store.Get(context.Background(), "cas/outside-link")
	require.Error(t, err)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.List(cancelled, "cas", "", 10)
	require.ErrorIs(t, err, context.Canceled)
}

func TestFilesystemBlobStoreConcurrentIdempotentPutAndSelfCheckCleanup(t *testing.T) {
	store, err := NewFilesystemBlobStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	payload := []byte("same encrypted body")
	var wait sync.WaitGroup
	errorsByWriter := make([]error, 8)
	for index := range errorsByWriter {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsByWriter[index] = store.Put(context.Background(), "cas/v1/key/concurrent.sar", bytes.NewReader(payload), int64(len(payload)))
		}()
	}
	wait.Wait()
	for _, err := range errorsByWriter {
		require.NoError(t, err)
	}
	require.NoError(t, store.SelfCheck(context.Background()))
	page, err := store.List(context.Background(), "self-check", "", 10)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		require.NoError(t, err)
	}
	require.Empty(t, page.Keys)
}
