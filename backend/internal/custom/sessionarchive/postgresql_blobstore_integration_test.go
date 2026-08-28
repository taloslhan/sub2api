//go:build integration

package sessionarchive

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPostgreSQLBlobStoreContract(t *testing.T) {
	_, err := sessionArchiveIntegrationDB.Exec("TRUNCATE session_archive_pg_objects CASCADE")
	require.NoError(t, err)
	store, err := NewPostgreSQLBlobStore(sessionArchiveIntegrationDB, 64*1024)
	require.NoError(t, err)
	payload := bytes.Repeat([]byte("archive"), 20000)
	key := "cas/v1/key/cross-chunk.sar"
	require.NoError(t, store.Put(context.Background(), key, bytes.NewReader(payload), int64(len(payload))))
	require.NoError(t, store.Put(context.Background(), key, bytes.NewReader(payload), int64(len(payload))))

	reader, err := store.Get(context.Background(), key)
	require.NoError(t, err)
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, payload, got)

	require.NoError(t, store.Put(context.Background(), "cas/v1/key/empty.sar", bytes.NewReader(nil), 0))
	emptyReader, err := store.Get(context.Background(), "cas/v1/key/empty.sar")
	require.NoError(t, err)
	empty, err := io.ReadAll(emptyReader)
	require.NoError(t, err)
	require.NoError(t, emptyReader.Close())
	require.Empty(t, empty)
	first, err := store.List(context.Background(), "cas/v1", "", 1)
	require.NoError(t, err)
	require.Len(t, first.Keys, 1)
	require.NotEmpty(t, first.NextCursor)
	second, err := store.List(context.Background(), "cas/v1", first.NextCursor, 10)
	require.NoError(t, err)
	require.Len(t, second.Keys, 1)

	require.NoError(t, store.Delete(context.Background(), key))
	require.NoError(t, store.Delete(context.Background(), key))
	var chunks int
	require.NoError(t, sessionArchiveIntegrationDB.QueryRow("SELECT COUNT(*) FROM session_archive_pg_object_chunks WHERE object_key=$1", key).Scan(&chunks))
	require.Zero(t, chunks)
	require.NoError(t, store.SelfCheck(context.Background()))
}

func TestPostgreSQLBlobStoreRollsBackBadSizesAndReleasesConnectionOnClose(t *testing.T) {
	_, err := sessionArchiveIntegrationDB.Exec("TRUNCATE session_archive_pg_objects CASCADE")
	require.NoError(t, err)
	store, err := NewPostgreSQLBlobStore(sessionArchiveIntegrationDB, 64*1024)
	require.NoError(t, err)
	require.Error(t, store.Put(context.Background(), "cas/v1/key/short.sar", bytes.NewReader([]byte("x")), 2))
	require.Error(t, store.Put(context.Background(), "cas/v1/key/long.sar", bytes.NewReader([]byte("xx")), 1))
	var objects int
	require.NoError(t, sessionArchiveIntegrationDB.QueryRow("SELECT COUNT(*) FROM session_archive_pg_objects").Scan(&objects))
	require.Zero(t, objects)

	payload := bytes.Repeat([]byte("x"), 130000)
	require.NoError(t, store.Put(context.Background(), "cas/v1/key/close.sar", bytes.NewReader(payload), int64(len(payload))))
	reader, err := store.Get(context.Background(), "cas/v1/key/close.sar")
	require.NoError(t, err)
	buffer := make([]byte, 10)
	_, err = reader.Read(buffer)
	require.NoError(t, err)
	require.NoError(t, reader.Close())

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelledReader, err := store.Get(cancelCtx, "cas/v1/key/close.sar")
	require.NoError(t, err)
	_, err = cancelledReader.Read(buffer)
	require.NoError(t, err)
	cancel()
	_, err = cancelledReader.Read(buffer)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, cancelledReader.Close())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, sessionArchiveIntegrationDB.PingContext(ctx))
}
