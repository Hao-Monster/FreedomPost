-- Keep legacy URL tombstones after the private attachment has been cleaned up.
-- Cleanup must wait for both retirement and cache confirmation; completed audit
-- records are independent of the attachment's storage/quota lifetime.
ALTER TABLE managed_media_legacy_sources
  DROP CONSTRAINT managed_media_legacy_sources_attachment_id_fkey;
