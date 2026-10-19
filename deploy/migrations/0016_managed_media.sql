-- New article/comment objects are stored outside the public upload origin.
-- Existing URLs remain legacy_public until an explicit migration is applied.
ALTER TABLE attachments
  ADD COLUMN IF NOT EXISTS delivery_mode text NOT NULL DEFAULT 'legacy_public',
  ADD COLUMN IF NOT EXISTS context_post_id uuid REFERENCES posts(id) ON DELETE SET NULL;
ALTER TABLE attachments ADD CONSTRAINT attachments_delivery_mode_check
  CHECK (delivery_mode IN ('legacy_public', 'managed'));

-- A file can be deliberately reused by several articles. Each delivery URL
-- identifies one article and is authorized against that article at read time.
CREATE TABLE attachment_post_refs (
  attachment_id uuid NOT NULL REFERENCES attachments(id) ON DELETE CASCADE,
  post_id uuid NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
  PRIMARY KEY (attachment_id, post_id)
);
CREATE INDEX attachment_post_refs_post_id_idx ON attachment_post_refs(post_id);

-- Copying is reversible and never implicitly deletes a historically public
-- object. Retirement and cache purge are separate, explicitly approved steps.
CREATE TABLE managed_media_legacy_sources (
  attachment_id uuid PRIMARY KEY REFERENCES attachments(id) ON DELETE RESTRICT,
  storage_provider text NOT NULL,
  storage_key text NOT NULL,
  public_url text NOT NULL,
  sha256 text NOT NULL,
  copied_at timestamptz NOT NULL DEFAULT NOW(),
  retired_at timestamptz,
  cache_purged_at timestamptz
);
