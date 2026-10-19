ALTER TABLE attachments
  ADD COLUMN upload_state TEXT NOT NULL DEFAULT 'ready'
    CHECK (upload_state IN ('uploading', 'ready', 'deleting')),
  ADD COLUMN uploader_ip_hash TEXT;

-- Old unclaimed comment uploads had no expiry. Give them a full grace period;
-- claimed comments are untouched. Cleanup retains metadata until deletion succeeds.
UPDATE attachments SET pending_until = NOW() + INTERVAL '24 hours'
WHERE owner_type = 'comment' AND owner_id IS NULL
  AND uploader_type = 'public-comment' AND pending_until IS NULL;

CREATE INDEX attachments_pending_cleanup_idx ON attachments(pending_until)
WHERE owner_id IS NULL AND pending_until IS NOT NULL;
CREATE INDEX attachments_pending_comment_ip_idx ON attachments(uploader_ip_hash)
WHERE owner_type = 'comment' AND owner_id IS NULL AND uploader_type = 'public-comment';
