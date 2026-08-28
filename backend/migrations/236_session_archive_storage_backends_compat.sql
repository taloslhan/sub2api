-- CAPYBARA-PATCH: Session Archive 多存储第一阶段滚动兼容层。
-- 旧三列/单列唯一约束必须保留到所有实例都改用 backend-aware SQL 后。

ALTER TABLE session_archive_blobs
    ADD COLUMN IF NOT EXISTS storage_backend VARCHAR(32) NOT NULL DEFAULT 's3';

UPDATE session_archive_blobs SET storage_backend = 's3' WHERE storage_backend IS NULL;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_session_archive_blob_storage_backend' AND conrelid = 'session_archive_blobs'::regclass) THEN
        ALTER TABLE session_archive_blobs
            ADD CONSTRAINT chk_session_archive_blob_storage_backend
            CHECK (storage_backend IN ('s3', 'filesystem', 'postgresql'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'uq_session_archive_blobs_backend_cas' AND conrelid = 'session_archive_blobs'::regclass) THEN
        ALTER TABLE session_archive_blobs
            ADD CONSTRAINT uq_session_archive_blobs_backend_cas
            UNIQUE (storage_backend, stored_plaintext_sha256, format_version, key_id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'uq_session_archive_blobs_backend_object_key' AND conrelid = 'session_archive_blobs'::regclass) THEN
        ALTER TABLE session_archive_blobs
            ADD CONSTRAINT uq_session_archive_blobs_backend_object_key
            UNIQUE (storage_backend, object_key);
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS session_archive_pg_objects (
    object_key TEXT PRIMARY KEY,
    total_bytes BIGINT NOT NULL,
    chunk_count INT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_session_archive_pg_object_sizes CHECK (total_bytes >= 0 AND chunk_count >= 0)
);

CREATE TABLE IF NOT EXISTS session_archive_pg_object_chunks (
    object_key TEXT NOT NULL REFERENCES session_archive_pg_objects(object_key) ON DELETE CASCADE,
    sequence_no INT NOT NULL,
    data BYTEA NOT NULL,
    PRIMARY KEY (object_key, sequence_no),
    CONSTRAINT chk_session_archive_pg_chunk_sequence CHECK (sequence_no >= 0),
    CONSTRAINT chk_session_archive_pg_chunk_size CHECK (octet_length(data) <= 8388608)
);

ALTER TABLE session_archive_deletion_jobs
    ADD COLUMN IF NOT EXISTS next_retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE INDEX IF NOT EXISTS idx_session_archive_deletion_jobs_retry
    ON session_archive_deletion_jobs(status, next_retry_at, id)
    WHERE status IN ('pending', 'running');
