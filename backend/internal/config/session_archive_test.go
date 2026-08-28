package config

import (
	"encoding/base64"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestSessionArchiveDefaultsAreDisabledAndBounded(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.False(t, cfg.SessionArchive.Enabled)
	require.Equal(t, 4, cfg.SessionArchive.WorkerCount)
	require.Equal(t, 512, cfg.SessionArchive.QueueSize)
	require.Equal(t, int64(256*1024*1024), cfg.SessionArchive.QueueMaxBytes)
	require.Equal(t, int64(64*1024*1024), cfg.SessionArchive.PayloadMaxBytes)
	require.Equal(t, 30, cfg.SessionArchive.DefaultRetentionDays)
	require.Equal(t, 300, cfg.SessionArchive.MergeWindowSeconds)
}

func TestLoadSessionArchiveScalarsAndCredentialsFromEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	viper.Set("session_archive.encryption_keys", map[string]string{"v1": key})
	t.Setenv("SESSION_ARCHIVE_ENABLED", "true")
	t.Setenv("SESSION_ARCHIVE_WORKER_COUNT", "2")
	t.Setenv("SESSION_ARCHIVE_QUEUE_SIZE", "64")
	t.Setenv("SESSION_ARCHIVE_QUEUE_MAX_BYTES", "1048576")
	t.Setenv("SESSION_ARCHIVE_PAYLOAD_MAX_BYTES", "524288")
	t.Setenv("SESSION_ARCHIVE_S3_ENDPOINT", "http://127.0.0.1:9000")
	t.Setenv("SESSION_ARCHIVE_S3_BUCKET", "private-archive")
	t.Setenv("SESSION_ARCHIVE_S3_ACCESS_KEY_ID", "access")
	t.Setenv("SESSION_ARCHIVE_S3_SECRET_ACCESS_KEY", "secret")
	t.Setenv("SESSION_ARCHIVE_ACTIVE_KEY_ID", "v1")

	cfg, err := Load()
	require.NoError(t, err)
	require.True(t, cfg.SessionArchive.Enabled)
	require.Equal(t, 2, cfg.SessionArchive.WorkerCount)
	require.Equal(t, "private-archive", cfg.SessionArchive.S3.Bucket)
	require.Equal(t, "v1", cfg.SessionArchive.ActiveKeyID)
	keys, err := cfg.SessionArchive.DecodedEncryptionKeys()
	require.NoError(t, err)
	require.Len(t, keys["v1"], 32)
}

func TestSessionArchiveValidationOnlyRequiresStorageWhenEnabled(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	cfg.SessionArchive.S3.Endpoint = "http://example.com"
	require.NoError(t, cfg.Validate())

	cfg.SessionArchive.Enabled = true
	err = cfg.Validate()
	require.ErrorContains(t, err, "HTTPS")
}

func TestSessionArchiveRejectsInvalidPersistentKey(t *testing.T) {
	resetViperWithJWTSecret(t)
	viper.Set("session_archive.enabled", true)
	viper.Set("session_archive.s3.endpoint", "https://s3.example.com")
	viper.Set("session_archive.s3.bucket", "private")
	viper.Set("session_archive.s3.access_key_id", "access")
	viper.Set("session_archive.s3.secret_access_key", "secret")
	viper.Set("session_archive.active_key_id", "v1")
	viper.Set("session_archive.encryption_keys", map[string]string{"v1": base64.StdEncoding.EncodeToString([]byte("too-short"))})
	_, err := Load()
	require.ErrorContains(t, err, "base64-encoded 32 bytes")
}
