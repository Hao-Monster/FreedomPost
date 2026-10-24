ALTER TABLE attachments
  ADD COLUMN upload_state TEXT NOT NULL DEFAULT 'ready'
    CHECK (upload_state IN ('uploading', 'ready', 'deleting')),
  ADD COLUMN uploader_ip_hash TEXT;

-- Old unclaimed comment uploads had no expiry. Give them a full grace period;
-- claimed comments are untouched. Cleanup retains metadata until deletion succeeds.
UPDATE attachments a SET pending_until = NOW() + INTERVAL '24 hours'
WHERE a.owner_type = 'comment' AND a.owner_id IS NULL
  AND a.uploader_type = 'public-comment' AND a.pending_until IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM attachment_post_refs r WHERE r.attachment_id = a.id
  )
  AND NOT EXISTS (
    SELECT 1 FROM (
      SELECT COALESCE((
        SELECT lower(translate(string_agg(
          CASE
            WHEN m.tok[1] ~ '^%[0-9a-fA-F]{2}$' AND ('x' || substr(m.tok[1], 2))::bit(8)::int BETWEEN 1 AND 127
              THEN chr(('x' || substr(m.tok[1], 2))::bit(8)::int)
            ELSE m.tok[1]
          END,
          '' ORDER BY m.ord
        ), E'\\', '/'))
        FROM regexp_matches(p.content_html || E'\n' || p.content_markdown, '%[0-9a-fA-F]{2}|[^%]+|%', 'g') WITH ORDINALITY AS m(tok, ord)
      ), '') AS normalized_body
      FROM posts p
    ) p
    WHERE (a.stored_filename <> '' AND strpos(p.normalized_body, lower(a.stored_filename)) > 0)
       OR (a.public_url <> '' AND strpos(p.normalized_body, lower(translate(a.public_url, E'\\', '/'))) > 0)
  )
  AND NOT EXISTS (
    SELECT 1 FROM (
      SELECT COALESCE((
        SELECT lower(translate(string_agg(
          CASE
            WHEN m.tok[1] ~ '^%[0-9a-fA-F]{2}$' AND ('x' || substr(m.tok[1], 2))::bit(8)::int BETWEEN 1 AND 127
              THEN chr(('x' || substr(m.tok[1], 2))::bit(8)::int)
            ELSE m.tok[1]
          END,
          '' ORDER BY m.ord
        ), E'\\', '/'))
        FROM regexp_matches(to_jsonb(p)::text, '%[0-9a-fA-F]{2}|[^%]+|%', 'g') WITH ORDINALITY AS m(tok, ord)
      ), '') AS normalized_body
      FROM products p
    ) p
    WHERE (a.stored_filename <> '' AND strpos(p.normalized_body, lower(a.stored_filename)) > 0)
       OR (a.public_url <> '' AND strpos(p.normalized_body, lower(translate(a.public_url, E'\\', '/'))) > 0)
  )
  AND NOT EXISTS (
    SELECT 1 FROM (
      SELECT COALESCE((
        SELECT lower(translate(string_agg(
          CASE
            WHEN m.tok[1] ~ '^%[0-9a-fA-F]{2}$' AND ('x' || substr(m.tok[1], 2))::bit(8)::int BETWEEN 1 AND 127
              THEN chr(('x' || substr(m.tok[1], 2))::bit(8)::int)
            ELSE m.tok[1]
          END,
          '' ORDER BY m.ord
        ), E'\\', '/'))
        FROM regexp_matches(to_jsonb(t)::text, '%[0-9a-fA-F]{2}|[^%]+|%', 'g') WITH ORDINALITY AS m(tok, ord)
      ), '') AS normalized_body
      FROM tools t
    ) t
    WHERE (a.stored_filename <> '' AND strpos(t.normalized_body, lower(a.stored_filename)) > 0)
       OR (a.public_url <> '' AND strpos(t.normalized_body, lower(translate(a.public_url, E'\\', '/'))) > 0)
  );

CREATE INDEX attachments_pending_cleanup_idx ON attachments(pending_until)
WHERE owner_id IS NULL AND pending_until IS NOT NULL;
CREATE INDEX attachments_pending_comment_ip_idx ON attachments(uploader_ip_hash)
WHERE owner_type = 'comment' AND owner_id IS NULL AND uploader_type = 'public-comment';
