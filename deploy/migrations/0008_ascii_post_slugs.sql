BEGIN;

CREATE TABLE IF NOT EXISTS post_slug_aliases (
  slug VARCHAR(32) PRIMARY KEY,
  post_id UUID NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_post_slug_aliases_post_id
ON post_slug_aliases(post_id);

INSERT INTO post_slug_aliases (slug, post_id)
SELECT slug, id
FROM posts
ON CONFLICT (slug) DO UPDATE SET post_id = EXCLUDED.post_id;

UPDATE posts
SET slug = 'tmp-' || substring(replace(id::text, '-', '') FROM 1 FOR 28);

CREATE TEMP TABLE migration_0008_post_slugs (
  id UUID PRIMARY KEY,
  slug VARCHAR(10) NOT NULL UNIQUE
) ON COMMIT DROP;

INSERT INTO migration_0008_post_slugs (id, slug)
SELECT
  id,
  'p_' || substring(
      translate(
        encode(digest(id::text || ':freedompost-post-slug-v1', 'sha256'), 'base64'),
        '+/',
        '-_'
      )
      FROM 1 FOR 8
    )
FROM posts;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM migration_0008_post_slugs AS generated
    INNER JOIN post_slug_aliases AS alias ON alias.slug = generated.slug
    WHERE alias.post_id <> generated.id
  ) THEN
    RAISE EXCEPTION 'post slug migration collides with a legacy alias';
  END IF;
END
$$;

UPDATE posts
SET slug = generated.slug
FROM migration_0008_post_slugs AS generated
WHERE posts.id = generated.id;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM posts
    WHERE slug !~ '^p_[A-Za-z0-9_-]{8}$'
  ) THEN
    RAISE EXCEPTION 'post slug migration produced a non-canonical slug';
  END IF;
END
$$;

COMMIT;
