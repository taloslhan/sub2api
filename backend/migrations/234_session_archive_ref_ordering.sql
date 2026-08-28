-- CAPYBARA-PATCH: 保留双向流事件的方向与发生时间，支持按全局事件序号重建。

ALTER TABLE session_archive_blob_refs
    ADD COLUMN IF NOT EXISTS direction VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS occurred_at TIMESTAMPTZ;

UPDATE session_archive_blob_refs
SET occurred_at = created_at
WHERE occurred_at IS NULL;

ALTER TABLE session_archive_blob_refs
    ALTER COLUMN occurred_at SET DEFAULT NOW(),
    ALTER COLUMN occurred_at SET NOT NULL;

-- 保留 231 建立的 (owner_type, owner_id, purpose, sequence_no) 唯一约束；
-- AddBlobRef 的 ON CONFLICT 必须与该约束逐列匹配。direction 仅作为重建元数据，
-- 协议适配层负责为同一 owner/purpose 分配全局递增 sequence_no。
