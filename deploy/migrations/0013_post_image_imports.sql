-- Clipboard-imported images are staged only until the article is saved. This
-- prevents abandoned paste attempts from becoming permanent orphaned objects.
ALTER TABLE attachments
  ADD COLUMN IF NOT EXISTS pending_until TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS source_host TEXT;

CREATE INDEX IF NOT EXISTS idx_attachments_pending_post_image_imports
  ON attachments (pending_until)
  WHERE owner_type = 'post'
    AND owner_id IS NULL
    AND uploader_type = 'admin-image-import'
    AND pending_until IS NOT NULL;
