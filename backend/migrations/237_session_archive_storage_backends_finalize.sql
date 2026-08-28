-- CAPYBARA-PATCH: Session Archive 多存储第二阶段约束收口。
-- 只能在所有实例均已使用四列 ReserveBlob SQL 后发布。

DO $$
DECLARE
    constraint_name TEXT;
    legacy_cas_attnums SMALLINT[];
    legacy_object_attnums SMALLINT[];
BEGIN
    SELECT array_agg(attnum::SMALLINT ORDER BY requested.ordinality)
      INTO legacy_cas_attnums
      FROM unnest(ARRAY['stored_plaintext_sha256', 'format_version', 'key_id']) WITH ORDINALITY AS requested(name, ordinality)
      JOIN pg_attribute ON attrelid = 'session_archive_blobs'::regclass AND attname = requested.name;

    SELECT ARRAY[attnum::SMALLINT]
      INTO legacy_object_attnums
      FROM pg_attribute
     WHERE attrelid = 'session_archive_blobs'::regclass AND attname = 'object_key';

    FOR constraint_name IN
        SELECT conname
          FROM pg_constraint
         WHERE conrelid = 'session_archive_blobs'::regclass
           AND contype = 'u'
           AND conkey = legacy_cas_attnums
           AND conname <> 'uq_session_archive_blobs_backend_cas'
    LOOP
        EXECUTE format('ALTER TABLE session_archive_blobs DROP CONSTRAINT %I', constraint_name);
    END LOOP;

    FOR constraint_name IN
        SELECT conname
          FROM pg_constraint
         WHERE conrelid = 'session_archive_blobs'::regclass
           AND contype = 'u'
           AND conkey = legacy_object_attnums
           AND conname <> 'uq_session_archive_blobs_backend_object_key'
    LOOP
        EXECUTE format('ALTER TABLE session_archive_blobs DROP CONSTRAINT %I', constraint_name);
    END LOOP;
END $$;
