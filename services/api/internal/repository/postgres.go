// Package repository implements the domain.Repository interface using PostgreSQL
// via pgxpool. All SQL is written by hand — no ORM, no sqlc — matching the
// style of services/paid-access/internal/store/postgres.go.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/security"
)

const (
	postViewIncrement = 5 // match TS POST_VIEW_INCREMENT
	maxConns          = 20
)

// Postgres implements domain.Repository.
type Postgres struct {
	pool *pgxpool.Pool
}

// New creates a Postgres repository with a connection pool.
func New(ctx context.Context, databaseURL string) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	cfg.MaxConns = maxConns
	cfg.HealthCheckPeriod = 30 * time.Second
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	var pool *pgxpool.Pool
	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			if pingErr := pool.Ping(ctx); pingErr == nil {
				lastErr = nil
				break
			} else {
				lastErr = pingErr
				pool.Close()
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("ping database after 30 attempts: %w", lastErr)
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }
func (p *Postgres) Close()                         { p.pool.Close() }

// ─── IncrementViewCount (used by viewcount buffer) ───────────────────────────

func (p *Postgres) IncrementViewCount(ctx context.Context, postSlug string, delta int) (int, error) {
	var viewCount int
	err := p.pool.QueryRow(ctx,
		`UPDATE posts SET view_count = view_count + $1 WHERE slug = $2
		 RETURNING view_count`,
		delta, postSlug,
	).Scan(&viewCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return viewCount, err
}

// ─── Posts ────────────────────────────────────────────────────────────────────

func (p *Postgres) ListPosts(ctx context.Context) ([]domain.Post, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, slug, title, visibility, price_cents, currency,
		        content_markdown, content_html, search_text, excerpt,
		        view_count, comment_count, attachment_count, created_at, updated_at
		 FROM posts ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list posts: %w", err)
	}
	defer rows.Close()
	return scanPosts(rows)
}

func (p *Postgres) ListPostSummaries(ctx context.Context) ([]domain.PostSummary, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT slug, title, visibility, price_cents, currency, excerpt,
		        view_count, comment_count, created_at, updated_at
		 FROM posts WHERE visibility != 'private' ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list post summaries: %w", err)
	}
	defer rows.Close()

	var posts []domain.PostSummary
	for rows.Next() {
		var p domain.PostSummary
		if err := rows.Scan(
			&p.Slug, &p.Title, &p.Visibility, &p.PriceCents, &p.Currency,
			&p.Excerpt, &p.ViewCount, &p.CommentCount, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	return posts, rows.Err()
}

func (p *Postgres) GetSearchDocuments(ctx context.Context) ([]domain.SearchDocument, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, slug, title, search_text, excerpt, updated_at
		 FROM posts WHERE visibility = 'public' ORDER BY updated_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("get search documents: %w", err)
	}
	defer rows.Close()

	var docs []domain.SearchDocument
	for rows.Next() {
		var d domain.SearchDocument
		if err := rows.Scan(&d.ID, &d.Slug, &d.Title, &d.Body, &d.Excerpt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

func (p *Postgres) GetPostBySlug(ctx context.Context, slug string) (*domain.Post, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT id, slug, title, visibility, price_cents, currency,
		        content_markdown, content_html, search_text, excerpt,
		        view_count, comment_count, attachment_count, created_at, updated_at
		 FROM posts
		 WHERE slug = $1`,
		slug,
	)
	return scanPost(row)
}

func (p *Postgres) GetPostByID(ctx context.Context, id string) (*domain.Post, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT id, slug, title, visibility, price_cents, currency,
		        content_markdown, content_html, search_text, excerpt,
		        view_count, comment_count, attachment_count, created_at, updated_at
		 FROM posts WHERE id = $1`,
		id,
	)
	return scanPost(row)
}

func (p *Postgres) CreatePost(ctx context.Context, input domain.CreatePostInput) (*domain.Post, error) {
	id := uuid.NewString()
	slug, err := p.uniquePostSlug(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	row := p.pool.QueryRow(ctx,
		`INSERT INTO posts (id, slug, title, visibility, price_cents, currency,
		                    content_json, content_markdown, content_html, search_text, excerpt,
		                    view_count, comment_count, attachment_count, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,'{}', $7,$8,$9,$10, 0, 0, 0, $11,$12)
		 RETURNING id, slug, title, visibility, price_cents, currency,
		           content_markdown, content_html, search_text, excerpt,
		           view_count, comment_count, attachment_count, created_at, updated_at`,
		id, slug, input.Title, string(input.Visibility), input.PriceCents, input.Currency,
		input.ContentMarkdown, input.ContentHTML, input.SearchText, input.Excerpt,
		now, now,
	)
	return scanPost(row)
}

func (p *Postgres) UpdatePost(ctx context.Context, input domain.UpdatePostInput) (*domain.Post, error) {
	existing, err := p.GetPostByID(ctx, input.ID)
	if err != nil || existing == nil {
		return nil, err
	}

	// Slug changes: no alias table in schema, just update directly
	newSlug := input.Slug
	if newSlug == "" {
		newSlug = existing.Slug
	}

	row := p.pool.QueryRow(ctx,
		`UPDATE posts SET
		    title=$2, slug=$3, visibility=$4, price_cents=$5, currency=$6,
		    content_markdown=$7, content_html=$8, search_text=$9, excerpt=$10,
		    updated_at=$11
		 WHERE id=$1
		 RETURNING id, slug, title, visibility, price_cents, currency,
		           content_markdown, content_html, search_text, excerpt,
		           view_count, comment_count, attachment_count, created_at, updated_at`,
		input.ID, input.Title, newSlug, string(input.Visibility), input.PriceCents, input.Currency,
		input.ContentMarkdown, input.ContentHTML, input.SearchText, input.Excerpt,
		time.Now(),
	)
	return scanPost(row)
}

func (p *Postgres) DeletePost(ctx context.Context, id string) (bool, error) {
	ct, err := p.pool.Exec(ctx, `DELETE FROM posts WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("delete post: %w", err)
	}
	return ct.RowsAffected() > 0, nil
}

func (p *Postgres) RecordView(ctx context.Context, input domain.RecordViewInput) (*domain.RecordViewResult, error) {
	post, err := p.GetPostBySlug(ctx, input.PostSlug)
	if err != nil || post == nil {
		return nil, err
	}

	// Try to insert a view record (UNIQUE on post_id+view_date+visitor_key)
	ct, err := p.pool.Exec(ctx,
		`INSERT INTO post_views (post_id, view_date, visitor_key, ip_hash, fingerprint_hash, local_id_hash)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT DO NOTHING`,
		post.ID, input.ViewDate, input.VisitorKey, nullStr(input.IPHash),
		nullStr(input.FingerprintHash), nullStr(input.LocalIDHash),
	)
	if err != nil {
		return nil, fmt.Errorf("record view: %w", err)
	}

	if ct.RowsAffected() > 0 {
		var viewCount int
		err = p.pool.QueryRow(ctx,
			`UPDATE posts SET view_count = view_count + $1 WHERE id = $2 RETURNING view_count`,
			postViewIncrement, post.ID,
		).Scan(&viewCount)
		if err != nil {
			return nil, fmt.Errorf("update view count: %w", err)
		}
		return &domain.RecordViewResult{Counted: true, ViewCount: viewCount}, nil
	}

	return &domain.RecordViewResult{Counted: false, ViewCount: post.ViewCount}, nil
}

// ─── Comments ─────────────────────────────────────────────────────────────────

func (p *Postgres) ListComments(ctx context.Context, postSlug string) ([]domain.Comment, error) {
	post, err := p.GetPostBySlug(ctx, postSlug)
	if err != nil || post == nil {
		return nil, err
	}

	rows, err := p.pool.Query(ctx,
		`SELECT id, post_id, parent_id, root_id, depth, path,
		        username, content, created_at
		 FROM comments WHERE post_id = $1
		 ORDER BY created_at DESC, path`,
		post.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	defer rows.Close()

	var comments []domain.Comment
	var commentIDs []string
	for rows.Next() {
		var c domain.Comment
		c.PostSlug = postSlug
		if err := rows.Scan(
			&c.ID, new(string), &c.ParentID, &c.RootID, &c.Depth, &c.Path,
			&c.Username, &c.Content, &c.CreatedAt,
		); err != nil {
			return nil, err
		}
		c.Attachments = []domain.CommentAttachment{}
		comments = append(comments, c)
		commentIDs = append(commentIDs, c.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(commentIDs) == 0 {
		return comments, nil
	}

	// Fetch attachments for all comments
	attachments, err := p.listAttachmentsByOwner(ctx, "comment", commentIDs)
	if err != nil {
		return nil, err
	}
	attMap := make(map[string][]domain.CommentAttachment)
	for _, a := range attachments {
		if a.OwnerID != nil {
			attMap[*a.OwnerID] = append(attMap[*a.OwnerID], domain.CommentAttachment{
				ID:              a.ID,
				Name:            a.OriginalFilename,
				MimeType:        a.MimeType,
				SizeBytes:       a.SizeBytes,
				URL:             a.PublicURL,
				StorageProvider: a.StorageProvider,
				StorageKey:      a.StorageKey,
				StoredFilename:  a.StoredFilename,
				SHA256:          a.SHA256,
			})
		}
	}
	for i := range comments {
		if atts, ok := attMap[comments[i].ID]; ok {
			comments[i].Attachments = atts
		}
	}
	return comments, nil
}

func (p *Postgres) CreateComment(ctx context.Context, input domain.CreateCommentInput) (*domain.Comment, error) {
	post, err := p.GetPostBySlug(ctx, input.PostSlug)
	if err != nil || post == nil {
		return nil, err
	}

	// Load existing comments to compute path
	rows, err := p.pool.Query(ctx,
		`SELECT id, parent_id, root_id, depth, path FROM comments WHERE post_id = $1`,
		post.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("load comments for path: %w", err)
	}
	defer rows.Close()

	type minimal struct {
		id, parentID, rootID string
		depth                int
		path                 string
	}
	var existing []minimal
	for rows.Next() {
		var m minimal
		var parentID, rootID *string
		if err := rows.Scan(&m.id, &parentID, &rootID, &m.depth, &m.path); err != nil {
			return nil, err
		}
		if parentID != nil {
			m.parentID = *parentID
		}
		if rootID != nil {
			m.rootID = *rootID
		}
		existing = append(existing, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Find parent
	var parentID, rootID *string
	var depth int
	var path string

	if input.ParentID != "" {
		for _, c := range existing {
			if c.id == input.ParentID {
				parentID = &c.id
				if c.rootID != "" {
					rootID = &c.rootID
				} else {
					rootID = &c.id
				}
				depth = c.depth + 1
				// Count siblings under same parent
				siblingCount := 0
				for _, s := range existing {
					if s.parentID == c.id {
						siblingCount++
					}
				}
				path = c.path + "." + security.PadPath(siblingCount+1)
				break
			}
		}
	}
	if path == "" {
		// Top-level comment
		topCount := 0
		for _, c := range existing {
			if c.parentID == "" {
				topCount++
			}
		}
		path = security.PadPath(topCount + 1)
	}

	commentID := uuid.NewString()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var comment domain.Comment
	comment.PostSlug = input.PostSlug
	comment.ParentID = parentID
	comment.RootID = rootID
	comment.Depth = depth
	comment.Path = path
	comment.Username = input.Username
	comment.Content = input.Content
	comment.Attachments = []domain.CommentAttachment{}

	if err := tx.QueryRow(ctx,
		`INSERT INTO comments (id, post_id, parent_id, root_id, depth, path,
		                       username, fingerprint_hash, local_id_hash, ip_hash,
		                       content, attachment_count, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		 RETURNING id, created_at`,
		commentID, post.ID, parentID, rootID, depth, path,
		input.Username, nullStr(input.FingerprintHash), nullStr(input.LocalIDHash),
		nullStr(input.IPHash), input.Content, len(input.Attachments), time.Now(),
	).Scan(&comment.ID, &comment.CreatedAt); err != nil {
		return nil, fmt.Errorf("insert comment: %w", err)
	}

	// Insert attachments
	for _, a := range input.Attachments {
		attID := uuid.NewString()
		if _, err := tx.Exec(ctx,
			`INSERT INTO attachments (id, owner_type, owner_id, uploader_type,
			                          original_filename, stored_filename, storage_provider,
			                          storage_key, public_url, mime_type, detected_mime_type,
			                          size_bytes, sha256, created_at)
			 VALUES ($1,'comment',$2,'public-comment',$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			attID, commentID, a.Name, a.StoredFilename, a.StorageProvider,
			a.StorageKey, a.URL, a.MimeType, a.MimeType, a.SizeBytes, a.SHA256, time.Now(),
		); err != nil {
			return nil, fmt.Errorf("insert attachment: %w", err)
		}
		comment.Attachments = append(comment.Attachments, domain.CommentAttachment{
			ID:              attID,
			Name:            a.Name,
			MimeType:        a.MimeType,
			SizeBytes:       a.SizeBytes,
			URL:             a.URL,
			StorageProvider: a.StorageProvider,
			StorageKey:      a.StorageKey,
			StoredFilename:  a.StoredFilename,
			SHA256:          a.SHA256,
		})
	}

	// Increment post comment count
	if _, err := tx.Exec(ctx,
		`UPDATE posts SET comment_count = comment_count + 1 WHERE id = $1`, post.ID,
	); err != nil {
		return nil, fmt.Errorf("update comment count: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit comment tx: %w", err)
	}
	return &comment, nil
}

// ─── Attachments ──────────────────────────────────────────────────────────────

func (p *Postgres) CreateAttachment(ctx context.Context, a domain.Attachment) (*domain.Attachment, error) {
	id := uuid.NewString()
	now := time.Now()
	err := p.pool.QueryRow(ctx,
		`INSERT INTO attachments (id, owner_type, owner_id, uploader_type,
		                          original_filename, stored_filename, storage_provider,
		                          storage_key, public_url, mime_type, detected_mime_type,
		                          size_bytes, sha256, created_at)
		 VALUES ($1,$2,$3,'admin',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		 RETURNING id`,
		id, a.OwnerType, a.OwnerID, a.OriginalFilename, a.StoredFilename, a.StorageProvider,
		a.StorageKey, a.PublicURL, a.MimeType, a.DetectedMime, a.SizeBytes, a.SHA256, now,
	).Scan(&a.ID)
	if err != nil {
		return nil, fmt.Errorf("create attachment: %w", err)
	}
	a.ID = id
	a.CreatedAt = now
	return &a, nil
}

func (p *Postgres) DeleteAttachmentsByIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	_, err := p.pool.Exec(ctx,
		`DELETE FROM attachments WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...,
	)
	return err
}

func (p *Postgres) listAttachmentsByOwner(ctx context.Context, ownerType string, ownerIDs []string) ([]domain.Attachment, error) {
	if len(ownerIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ownerIDs))
	args := []any{ownerType}
	for i, id := range ownerIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args = append(args, id)
	}
	rows, err := p.pool.Query(ctx,
		`SELECT id, owner_type, owner_id, original_filename, stored_filename,
		        storage_provider, storage_key, public_url, mime_type,
		        COALESCE(detected_mime_type, mime_type), size_bytes, COALESCE(sha256,''), created_at
		 FROM attachments WHERE owner_type = $1 AND owner_id IN (`+strings.Join(placeholders, ",")+`)
		 ORDER BY created_at`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var atts []domain.Attachment
	for rows.Next() {
		var a domain.Attachment
		if err := rows.Scan(
			&a.ID, &a.OwnerType, &a.OwnerID, &a.OriginalFilename, &a.StoredFilename,
			&a.StorageProvider, &a.StorageKey, &a.PublicURL, &a.MimeType,
			&a.DetectedMime, &a.SizeBytes, &a.SHA256, &a.CreatedAt,
		); err != nil {
			return nil, err
		}
		atts = append(atts, a)
	}
	return atts, rows.Err()
}

// ─── Products ─────────────────────────────────────────────────────────────────

func (p *Postgres) ListProducts(ctx context.Context, publishedOnly bool) ([]domain.Product, error) {
	q := `SELECT id, slug, title, description, cover_url, '' AS link_url,
		         price_cents, currency, commission_cents, status, sort_order,
		         created_at, updated_at FROM products`
	var args []any
	if publishedOnly {
		q += ` WHERE status = 'published'`
	}
	q += ` ORDER BY sort_order DESC, created_at DESC`
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	defer rows.Close()
	return scanProducts(rows)
}

func (p *Postgres) GetProductBySlug(ctx context.Context, slug string) (*domain.Product, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT id, slug, title, description, cover_url, '' AS link_url,
		        price_cents, currency, commission_cents, status, sort_order,
		        created_at, updated_at FROM products WHERE slug = $1`,
		slug,
	)
	return scanProduct(row)
}

func (p *Postgres) GetProductByID(ctx context.Context, id string) (*domain.Product, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT id, slug, title, description, cover_url, '' AS link_url,
		        price_cents, currency, commission_cents, status, sort_order,
		        created_at, updated_at FROM products WHERE id = $1`,
		id,
	)
	return scanProduct(row)
}

func (p *Postgres) CreateProduct(ctx context.Context, input domain.ProductInput) (*domain.Product, error) {
	id := uuid.NewString()
	slug, err := p.uniqueProductSlug(ctx, input.Title)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	row := p.pool.QueryRow(ctx,
		`INSERT INTO products (id, slug, title, description, cover_url,
		                        price_cents, currency, commission_cents, status, sort_order,
		                        created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		 RETURNING id, slug, title, description, cover_url, '' AS link_url,
		           price_cents, currency, commission_cents, status, sort_order,
		           created_at, updated_at`,
		id, slug, coalesce(input.Title, "未命名商品"), input.Description, input.ImageURL,
		input.PriceCents, coalesce(input.Currency, "CNY"), input.CommissionCents,
		coalesce(input.Status, "draft"), input.SortOrder, now, now,
	)
	return scanProduct(row)
}

func (p *Postgres) UpdateProduct(ctx context.Context, id string, input domain.ProductInput) (*domain.Product, error) {
	row := p.pool.QueryRow(ctx,
		`UPDATE products SET
		    title=$2, description=$3, cover_url=$4,
		    price_cents=$5, currency=$6, commission_cents=$7,
		    status=$8, sort_order=$9, updated_at=$10
		 WHERE id=$1
		 RETURNING id, slug, title, description, cover_url, '' AS link_url,
		           price_cents, currency, commission_cents, status, sort_order,
		           created_at, updated_at`,
		id, coalesce(input.Title, "未命名商品"), input.Description, input.ImageURL,
		input.PriceCents, coalesce(input.Currency, "CNY"), input.CommissionCents,
		coalesce(input.Status, "draft"), input.SortOrder, time.Now(),
	)
	return scanProduct(row)
}

func (p *Postgres) DeleteProduct(ctx context.Context, id string) (bool, error) {
	ct, err := p.pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, id)
	return ct.RowsAffected() > 0, err
}

// ─── Tools ────────────────────────────────────────────────────────────────────

func (p *Postgres) ListTools(ctx context.Context, publishedOnly bool) ([]domain.Tool, error) {
	q := `SELECT id, slug, title, summary, description, category, url, COALESCE(cover_url, '') AS cover_url,
		         status, sort_order, created_at, updated_at FROM tools`
	if publishedOnly {
		q += ` WHERE status = 'published'`
	}
	q += ` ORDER BY sort_order DESC, created_at DESC`
	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	defer rows.Close()
	return scanTools(rows)
}

func (p *Postgres) GetToolByID(ctx context.Context, id string) (*domain.Tool, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT id, slug, title, summary, description, category, url, COALESCE(cover_url, '') AS cover_url,
		        status, sort_order, created_at, updated_at FROM tools WHERE id = $1`,
		id,
	)
	return scanTool(row)
}

func (p *Postgres) CreateTool(ctx context.Context, input domain.ToolInput) (*domain.Tool, error) {
	id := uuid.NewString()
	slug, err := p.uniqueToolSlug(ctx, input.Title)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	row := p.pool.QueryRow(ctx,
		`INSERT INTO tools (id, slug, title, summary, description, category, url, cover_url,
		                    status, sort_order, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		 RETURNING id, slug, title, summary, description, category, url, COALESCE(cover_url, '') AS cover_url,
		           status, sort_order, created_at, updated_at`,
		id, slug, coalesce(input.Title, "未命名工具"), input.Summary, input.Description,
		coalesce(input.Category, "other"), coalesce(input.LinkURL, "#"), input.IconURL,
		coalesce(input.Status, "draft"), input.SortOrder, now, now,
	)
	return scanTool(row)
}

func (p *Postgres) UpdateTool(ctx context.Context, id string, input domain.ToolInput) (*domain.Tool, error) {
	row := p.pool.QueryRow(ctx,
		`UPDATE tools SET
		    title=$2, summary=$3, description=$4, category=$5,
		    url=$6, cover_url=$7, status=$8, sort_order=$9, updated_at=$10
		 WHERE id=$1
		 RETURNING id, slug, title, summary, description, category, url, COALESCE(cover_url, '') AS cover_url,
		           status, sort_order, created_at, updated_at`,
		id, coalesce(input.Title, "未命名工具"), input.Summary, input.Description,
		coalesce(input.Category, "other"), coalesce(input.LinkURL, "#"), input.IconURL,
		coalesce(input.Status, "draft"), input.SortOrder, time.Now(),
	)
	return scanTool(row)
}

func (p *Postgres) DeleteTool(ctx context.Context, id string) (bool, error) {
	ct, err := p.pool.Exec(ctx, `DELETE FROM tools WHERE id = $1`, id)
	return ct.RowsAffected() > 0, err
}

// ─── Affiliates ───────────────────────────────────────────────────────────────

func (p *Postgres) ListAffiliates(ctx context.Context) ([]domain.AffiliateListItem, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT a.id, a.wechat_id, a.status, a.default_markup_percent, a.created_at, a.updated_at,
		        COUNT(ac.id)::int AS total_clicks,
		        COALESCE(SUM(ac.is_unique), 0)::int AS unique_clicks,
		        COUNT(ao.id)::int AS order_count
		 FROM affiliates a
		 LEFT JOIN affiliate_clicks ac ON ac.affiliate_id = a.id
		 LEFT JOIN affiliate_orders ao ON ao.affiliate_id = a.id
		 GROUP BY a.id ORDER BY a.created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list affiliates: %w", err)
	}
	defer rows.Close()

	var affiliates []domain.AffiliateListItem
	for rows.Next() {
		var item domain.AffiliateListItem
		if err := rows.Scan(
			&item.ID, &item.WechatID, &item.Status, &item.DefaultMarkupPercent,
			&item.CreatedAt, &item.UpdatedAt,
			&item.TotalClicks, &item.UniqueClicks, &item.OrderCount,
		); err != nil {
			return nil, err
		}
		affiliates = append(affiliates, item)
	}
	return affiliates, rows.Err()
}

func (p *Postgres) GetAffiliateByWechatID(ctx context.Context, wechatID string) (*domain.Affiliate, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT id, wechat_id, password_hash, status, default_markup_percent, created_at, updated_at
		 FROM affiliates WHERE wechat_id = $1`,
		wechatID,
	)
	return scanAffiliate(row)
}

func (p *Postgres) GetAffiliateByID(ctx context.Context, id string) (*domain.Affiliate, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT id, wechat_id, password_hash, status, default_markup_percent, created_at, updated_at
		 FROM affiliates WHERE id = $1`,
		id,
	)
	return scanAffiliate(row)
}

func (p *Postgres) CreateAffiliate(ctx context.Context, wechatID, passwordHash string) (*domain.Affiliate, error) {
	row := p.pool.QueryRow(ctx,
		`INSERT INTO affiliates (wechat_id, password_hash) VALUES ($1, $2)
		 RETURNING id, wechat_id, password_hash, status, default_markup_percent, created_at, updated_at`,
		wechatID, passwordHash,
	)
	return scanAffiliate(row)
}

func (p *Postgres) UpdateAffiliateStatus(ctx context.Context, id string, status domain.AffiliateStatus) (bool, error) {
	ct, err := p.pool.Exec(ctx,
		`UPDATE affiliates SET status = $2, updated_at = $3 WHERE id = $1`,
		id, string(status), time.Now(),
	)
	return ct.RowsAffected() > 0, err
}

func (p *Postgres) UpdateAffiliatePassword(ctx context.Context, id, passwordHash string) (bool, error) {
	ct, err := p.pool.Exec(ctx,
		`UPDATE affiliates SET password_hash = $2, updated_at = $3 WHERE id = $1`,
		id, passwordHash, time.Now(),
	)
	return ct.RowsAffected() > 0, err
}

func (p *Postgres) ListAffiliateProducts(ctx context.Context, affiliateID string) ([]domain.AffiliateProductView, error) {
	var defaultMarkup int
	if err := p.pool.QueryRow(ctx,
		`SELECT default_markup_percent FROM affiliates WHERE id = $1`, affiliateID,
	).Scan(&defaultMarkup); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	products, err := p.ListProducts(ctx, true)
	if err != nil {
		return nil, err
	}

	overrideRows, err := p.pool.Query(ctx,
		`SELECT product_id, markup_percent FROM affiliate_product_markups WHERE affiliate_id = $1`,
		affiliateID,
	)
	if err != nil {
		return nil, err
	}
	defer overrideRows.Close()
	overrides := make(map[string]int)
	for overrideRows.Next() {
		var pid string
		var pct int
		if err := overrideRows.Scan(&pid, &pct); err != nil {
			return nil, err
		}
		overrides[pid] = pct
	}

	views := make([]domain.AffiliateProductView, len(products))
	for i, prod := range products {
		markup := defaultMarkup
		if v, ok := overrides[prod.ID]; ok {
			markup = v
		}
		views[i] = domain.BuildAffiliateProductView(prod, markup)
	}
	return views, nil
}

func (p *Postgres) SetAffiliateMarkup(ctx context.Context, affiliateID string, productIDs []string, markupPercent int) error {
	if productIDs == nil {
		// Reset all overrides and set default
		_, err := p.pool.Exec(ctx,
			`DELETE FROM affiliate_product_markups WHERE affiliate_id = $1`, affiliateID,
		)
		if err != nil {
			return err
		}
		_, err = p.pool.Exec(ctx,
			`UPDATE affiliates SET default_markup_percent = $2, updated_at = $3 WHERE id = $1`,
			affiliateID, markupPercent, time.Now(),
		)
		return err
	}
	if len(productIDs) == 0 {
		return nil
	}
	for _, pid := range productIDs {
		if _, err := p.pool.Exec(ctx,
			`INSERT INTO affiliate_product_markups (affiliate_id, product_id, markup_percent, updated_at)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (affiliate_id, product_id) DO UPDATE
			 SET markup_percent = EXCLUDED.markup_percent, updated_at = EXCLUDED.updated_at`,
			affiliateID, pid, markupPercent, time.Now(),
		); err != nil {
			return err
		}
	}
	return nil
}

func (p *Postgres) GetAffiliateDashboard(ctx context.Context, affiliateID string) (*domain.AffiliateDashboard, error) {
	affiliate, err := p.GetAffiliateByID(ctx, affiliateID)
	if err != nil || affiliate == nil {
		return nil, err
	}

	var totalClicks, uniqueClicks int
	_ = p.pool.QueryRow(ctx,
		`SELECT COUNT(*)::int, COALESCE(SUM(is_unique), 0)::int
		 FROM affiliate_clicks WHERE affiliate_id = $1`,
		affiliateID,
	).Scan(&totalClicks, &uniqueClicks)

	orders, err := p.listOrdersFor(ctx, affiliateID)
	if err != nil {
		return nil, err
	}
	return &domain.AffiliateDashboard{
		Affiliate:    *affiliate,
		TotalClicks:  totalClicks,
		UniqueClicks: uniqueClicks,
		Orders:       orders,
	}, nil
}

func (p *Postgres) RecordAffiliateClick(ctx context.Context, wechatID, visitorKey, path string) (bool, bool, error) {
	affiliate, err := p.GetAffiliateByWechatID(ctx, wechatID)
	if err != nil {
		return false, false, err
	}
	if affiliate == nil || affiliate.Status != domain.AffiliateStatusActive {
		return false, false, nil
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	var recentID string
	err = p.pool.QueryRow(ctx,
		`SELECT id FROM affiliate_clicks
		 WHERE affiliate_id = $1 AND visitor_key = $2 AND clicked_at > $3
		 LIMIT 1`,
		affiliate.ID, visitorKey, cutoff,
	).Scan(&recentID)
	isUnique := errors.Is(err, pgx.ErrNoRows)

	isUniqueInt := 0
	if isUnique {
		isUniqueInt = 1
	}
	if _, err := p.pool.Exec(ctx,
		`INSERT INTO affiliate_clicks (affiliate_id, visitor_key, path, is_unique)
		 VALUES ($1, $2, $3, $4)`,
		affiliate.ID, visitorKey, path, isUniqueInt,
	); err != nil {
		return false, false, fmt.Errorf("record click: %w", err)
	}
	return true, isUnique, nil
}

// ─── Orders ───────────────────────────────────────────────────────────────────

func (p *Postgres) CreateAffiliateOrder(ctx context.Context, input domain.CreateOrderInput) (*domain.AffiliateOrder, error) {
	orderCode, err := p.uniqueOrderCode(ctx)
	if err != nil {
		return nil, err
	}
	product, err := p.GetProductByID(ctx, input.ProductID)
	if err != nil || product == nil {
		return nil, fmt.Errorf("product not found")
	}
	now := time.Now()
	var order domain.AffiliateOrder
	err = p.pool.QueryRow(ctx,
		`INSERT INTO affiliate_orders (order_code, affiliate_id, product_id, product_title,
		                               price_cents, commission_cents, currency, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 RETURNING id, order_code, affiliate_id, product_id, product_title, price_cents, commission_cents,
		           COALESCE(currency, 'CNY'), COALESCE(order_status, 'pending'),
		           COALESCE(commission_status, 'pending'), created_at, updated_at`,
		orderCode, input.AffiliateID, input.ProductID, product.Title,
		input.CustomerPriceCents, input.CommissionCents, product.Currency, now, now,
	).Scan(
		&order.ID, &order.OrderCode, &order.AffiliateID, &order.ProductID, &order.ProductTitle,
		&order.PriceCents, &order.CommissionCents, &order.Currency,
		&order.OrderStatus, &order.CommissionStatus, &order.CreatedAt, &order.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create affiliate order: %w", err)
	}
	order.CustomerPriceCents = order.PriceCents
	order.Status = order.OrderStatus
	order.BaseCommissionCents = input.BaseCommissionCents
	order.MarkupCommissionCents = input.MarkupCommissionCents
	return &order, nil
}

func (p *Postgres) ListAffiliateOrders(ctx context.Context) ([]domain.AffiliateOrder, error) {
	return p.listOrdersFor(ctx, "")
}

func (p *Postgres) UpdateAffiliateOrderStatus(ctx context.Context, id, status, notes string) (bool, error) {
	ct, err := p.pool.Exec(ctx,
		`UPDATE affiliate_orders SET order_status = $2, updated_at = $3 WHERE id = $1`,
		id, status, time.Now(),
	)
	return ct.RowsAffected() > 0, err
}

func (p *Postgres) listOrdersFor(ctx context.Context, affiliateID string) ([]domain.AffiliateOrder, error) {
	q := `SELECT ao.id, ao.order_code, ao.affiliate_id, COALESCE(a.wechat_id, ''),
	             COALESCE(ao.product_id::text, ''), COALESCE(ao.product_title, ''),
	             ao.price_cents, ao.commission_cents, COALESCE(ao.currency, 'CNY'),
		         COALESCE(ao.order_status, 'pending'), COALESCE(ao.commission_status, 'pending'),
		         ao.created_at, ao.updated_at
		  FROM affiliate_orders ao
		  LEFT JOIN affiliates a ON a.id = ao.affiliate_id`
	var args []any
	if affiliateID != "" {
		q += ` WHERE ao.affiliate_id = $1`
		args = append(args, affiliateID)
	}
	q += ` ORDER BY ao.created_at DESC`
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var orders []domain.AffiliateOrder
	for rows.Next() {
		var o domain.AffiliateOrder
		if err := rows.Scan(
			&o.ID, &o.OrderCode, &o.AffiliateID, &o.AffiliateWechatID,
			&o.ProductID, &o.ProductTitle,
			&o.PriceCents, &o.CommissionCents, &o.Currency,
			&o.OrderStatus, &o.CommissionStatus,
			&o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, err
		}
		o.CustomerPriceCents = o.PriceCents
		o.Status = o.OrderStatus
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

// ─── Benefit ──────────────────────────────────────────────────────────────────

func (p *Postgres) GetBenefitCampaign(ctx context.Context, id string) (*domain.BenefitCampaign, error) {
	var c domain.BenefitCampaign
	var startsAt, endsAt *time.Time
	err := p.pool.QueryRow(ctx,
		`SELECT id, name, enabled, starts_at, ends_at
		 FROM benefit_campaigns WHERE id = $1`,
		id,
	).Scan(&c.ID, &c.Name, &c.Enabled, &startsAt, &endsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if startsAt != nil {
		c.StartsAt = startsAt.UTC().Format(time.RFC3339)
	}
	if endsAt != nil {
		c.EndsAt = endsAt.UTC().Format(time.RFC3339)
	}
	return &c, nil
}

// scanBenefitClaim scans a single BenefitClaim row.
func scanBenefitClaim(row pgx.Row) (*domain.BenefitClaim, error) {
	var c domain.BenefitClaim
	var expiresAt *time.Time
	var opusUserID, opusDeviceID, subURLEnc *string
	err := row.Scan(
		&c.ID, &c.CampaignID, &c.ExternalClaimID, &c.BrowserKeyHash, &c.NetworkKeyHash,
		&c.Status, &opusUserID, &opusDeviceID, &subURLEnc, &expiresAt,
		&c.CreatedAt, &c.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if expiresAt != nil {
		c.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}
	if opusUserID != nil {
		c.OpusUserID = *opusUserID
	}
	if opusDeviceID != nil {
		c.OpusDeviceID = *opusDeviceID
	}
	if subURLEnc != nil {
		c.SubscriptionURLEnc = *subURLEnc
	}
	return &c, nil
}

const selectClaimCols = `id, campaign_id, external_claim_id, browser_key_hash, network_key_hash,
	status, opus_user_id, opus_device_id, subscription_url_enc, expires_at,
	created_at, updated_at`

func (p *Postgres) GetBenefitClaimByBrowserKey(ctx context.Context, campaignID, browserKeyHash string) (*domain.BenefitClaim, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT `+selectClaimCols+`
		 FROM benefit_claims WHERE campaign_id = $1 AND browser_key_hash = $2
		 ORDER BY created_at DESC LIMIT 1`,
		campaignID, browserKeyHash,
	)
	return scanBenefitClaim(row)
}

func (p *Postgres) GetBenefitClaimByID(ctx context.Context, id string) (*domain.BenefitClaim, error) {
	row := p.pool.QueryRow(ctx,
		`SELECT `+selectClaimCols+` FROM benefit_claims WHERE id = $1`, id,
	)
	return scanBenefitClaim(row)
}

func (p *Postgres) CreateBenefitClaim(ctx context.Context, input domain.CreateBenefitClaimInput) (*domain.CreateBenefitClaimResult, error) {
	id := uuid.NewString()
	now := time.Now()
	row := p.pool.QueryRow(ctx,
		`INSERT INTO benefit_claims
		   (id, campaign_id, external_claim_id, browser_key_hash, network_key_hash, status, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,'pending',$6,$6)
		 ON CONFLICT (campaign_id, browser_key_hash) DO NOTHING
		 RETURNING `+selectClaimCols,
		id, input.CampaignID, input.ExternalClaimID, input.BrowserKeyHash, input.NetworkKeyHash, now,
	)
	claim, err := scanBenefitClaim(row)
	if err != nil {
		return nil, err
	}
	if claim == nil {
		// Conflict: fetch existing
		existing, fetchErr := p.GetBenefitClaimByBrowserKey(ctx, input.CampaignID, input.BrowserKeyHash)
		if fetchErr != nil {
			return nil, fetchErr
		}
		return &domain.CreateBenefitClaimResult{Claim: existing, Created: false}, nil
	}
	return &domain.CreateBenefitClaimResult{Claim: claim, Created: true}, nil
}

func (p *Postgres) CountBenefitClaimsByNetworkSince(ctx context.Context, campaignID, networkKeyHash, since string) (int, error) {
	t, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return 0, fmt.Errorf("CountBenefitClaimsByNetworkSince: invalid since: %w", err)
	}
	var count int
	err = p.pool.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM benefit_claims
		 WHERE campaign_id = $1 AND network_key_hash = $2 AND created_at >= $3`,
		campaignID, networkKeyHash, t,
	).Scan(&count)
	return count, err
}

// BeginBenefitProvisioning atomically transitions a pending/failed claim to
// 'provisioning' using optimistic locking. Returns the updated claim or nil
// if the transition was not possible (race condition).
func (p *Postgres) BeginBenefitProvisioning(ctx context.Context, claimID string) (*domain.BenefitClaim, error) {
	now := time.Now()
	row := p.pool.QueryRow(ctx,
		`UPDATE benefit_claims
		 SET status = 'provisioning', attempt_count = attempt_count + 1, updated_at = $2
		 WHERE id = $1 AND status IN ('pending', 'failed')
		 RETURNING `+selectClaimCols,
		claimID, now,
	)
	return scanBenefitClaim(row)
}

// CompleteBenefitClaim atomically transitions a provisioning claim to 'ready'.
func (p *Postgres) CompleteBenefitClaim(ctx context.Context, claimID string, input domain.CompleteBenefitClaimInput) (*domain.BenefitClaim, error) {
	expiresAt, err := time.Parse(time.RFC3339, input.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("CompleteBenefitClaim: invalid expiresAt: %w", err)
	}
	now := time.Now()
	row := p.pool.QueryRow(ctx,
		`UPDATE benefit_claims
		 SET status = 'ready',
		     opus_user_id = $2,
		     opus_device_id = $3,
		     subscription_url_enc = $4,
		     expires_at = $5,
		     updated_at = $6
		 WHERE id = $1 AND status = 'provisioning'
		 RETURNING `+selectClaimCols,
		claimID, input.OpusUserID, input.OpusDeviceID, input.SubscriptionURLEnc, expiresAt, now,
	)
	return scanBenefitClaim(row)
}

// FailBenefitClaim transitions a provisioning claim back to 'failed'.
func (p *Postgres) FailBenefitClaim(ctx context.Context, claimID, errorCode string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE benefit_claims
		 SET status = 'failed', last_error_code = $2, updated_at = $3
		 WHERE id = $1`,
		claimID, errorCode, time.Now(),
	)
	return err
}

// RecoverStaleBenefitProvisioning recovers a claim stuck in 'provisioning'
// state for longer than olderThan, transitioning it back to 'pending'.
// Returns nil if the claim was not stale.
func (p *Postgres) RecoverStaleBenefitProvisioning(ctx context.Context, claimID, olderThan string) (*domain.BenefitClaim, error) {
	t, err := time.Parse(time.RFC3339, olderThan)
	if err != nil {
		return nil, fmt.Errorf("RecoverStaleBenefitProvisioning: invalid olderThan: %w", err)
	}
	now := time.Now()
	row := p.pool.QueryRow(ctx,
		`UPDATE benefit_claims
		 SET status = 'pending', updated_at = $3
		 WHERE id = $1 AND status = 'provisioning' AND updated_at < $2
		 RETURNING `+selectClaimCols,
		claimID, t, now,
	)
	return scanBenefitClaim(row)
}

// ─── Slug generators ─────────────────────────────────────────────────────────

func (p *Postgres) uniquePostSlug(ctx context.Context) (string, error) {
	for range 8 {
		slug, err := security.GenerateSlug()
		if err != nil {
			return "", err
		}
		post, err := p.GetPostBySlug(ctx, slug)
		if err != nil {
			return "", err
		}
		if post == nil {
			return slug, nil
		}
	}
	return "", fmt.Errorf("failed to generate unique post slug after 8 attempts")
}

func (p *Postgres) uniqueProductSlug(ctx context.Context, title string) (string, error) {
	base := makeSlug(coalesce(title, "product"))
	for i := range 5 {
		slug := base
		if i > 0 {
			slug = fmt.Sprintf("%s-%d", base, i+1)
		}
		prod, _ := p.GetProductBySlug(ctx, slug)
		if prod == nil {
			return slug, nil
		}
	}
	return base + "-" + uuid.NewString()[:8], nil
}

func (p *Postgres) uniqueToolSlug(ctx context.Context, title string) (string, error) {
	base := makeSlug(coalesce(title, "tool"))
	for i := range 5 {
		slug := base
		if i > 0 {
			slug = fmt.Sprintf("%s-%d", base, i+1)
		}
		var id string
		err := p.pool.QueryRow(ctx, `SELECT id FROM tools WHERE slug = $1`, slug).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return slug, nil
		}
		if err != nil {
			return "", err
		}
	}
	return base + "-" + uuid.NewString()[:8], nil
}

func (p *Postgres) uniqueOrderCode(ctx context.Context) (string, error) {
	for range 8 {
		code := "FP" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
		var id string
		err := p.pool.QueryRow(ctx, `SELECT id FROM affiliate_orders WHERE order_code = $1`, code).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return code, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("failed to generate unique order code")
}

// ─── Scan helpers ─────────────────────────────────────────────────────────────

func scanPost(row pgx.Row) (*domain.Post, error) {
	var post domain.Post
	err := row.Scan(
		&post.ID, &post.Slug, &post.Title, &post.Visibility, &post.PriceCents, &post.Currency,
		&post.ContentMarkdown, &post.ContentHTML, &post.SearchText, &post.Excerpt,
		&post.ViewCount, &post.CommentCount, &post.AttachmentCount, &post.CreatedAt, &post.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	post.Markdown = post.ContentMarkdown
	return &post, err
}

func scanPosts(rows pgx.Rows) ([]domain.Post, error) {
	var posts []domain.Post
	for rows.Next() {
		var post domain.Post
		if err := rows.Scan(
			&post.ID, &post.Slug, &post.Title, &post.Visibility, &post.PriceCents, &post.Currency,
			&post.ContentMarkdown, &post.ContentHTML, &post.SearchText, &post.Excerpt,
			&post.ViewCount, &post.CommentCount, &post.AttachmentCount, &post.CreatedAt, &post.UpdatedAt,
		); err != nil {
			return nil, err
		}
		post.Markdown = post.ContentMarkdown
		posts = append(posts, post)
	}
	return posts, rows.Err()
}

func scanProduct(row pgx.Row) (*domain.Product, error) {
	var prod domain.Product
	err := row.Scan(
		&prod.ID, &prod.Slug, &prod.Title, &prod.Description, &prod.ImageURL, &prod.LinkURL,
		&prod.PriceCents, &prod.Currency, &prod.CommissionCents, &prod.Status, &prod.SortOrder,
		&prod.CreatedAt, &prod.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &prod, err
}

func scanProducts(rows pgx.Rows) ([]domain.Product, error) {
	var prods []domain.Product
	for rows.Next() {
		var prod domain.Product
		if err := rows.Scan(
			&prod.ID, &prod.Slug, &prod.Title, &prod.Description, &prod.ImageURL, &prod.LinkURL,
			&prod.PriceCents, &prod.Currency, &prod.CommissionCents, &prod.Status, &prod.SortOrder,
			&prod.CreatedAt, &prod.UpdatedAt,
		); err != nil {
			return nil, err
		}
		prods = append(prods, prod)
	}
	return prods, rows.Err()
}

func scanTool(row pgx.Row) (*domain.Tool, error) {
	var tool domain.Tool
	err := row.Scan(
		&tool.ID, &tool.Slug, &tool.Title, &tool.Summary, &tool.Description,
		&tool.Category, &tool.LinkURL, &tool.IconURL,
		&tool.Status, &tool.SortOrder, &tool.CreatedAt, &tool.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tool.URL = tool.LinkURL
	tool.CoverURL = tool.IconURL
	return &tool, nil
}

func scanTools(rows pgx.Rows) ([]domain.Tool, error) {
	var tools []domain.Tool
	for rows.Next() {
		var tool domain.Tool
		if err := rows.Scan(
			&tool.ID, &tool.Slug, &tool.Title, &tool.Summary, &tool.Description,
			&tool.Category, &tool.LinkURL, &tool.IconURL,
			&tool.Status, &tool.SortOrder, &tool.CreatedAt, &tool.UpdatedAt,
		); err != nil {
			return nil, err
		}
		tool.URL = tool.LinkURL
		tool.CoverURL = tool.IconURL
		tools = append(tools, tool)
	}
	return tools, rows.Err()
}

func scanAffiliate(row pgx.Row) (*domain.Affiliate, error) {
	var a domain.Affiliate
	err := row.Scan(&a.ID, &a.WechatID, &a.PasswordHash, &a.Status, &a.DefaultMarkupPercent, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}

// ─── DB utility helpers ───────────────────────────────────────────────────────

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func tagsToArray(tags []string) string {
	return "{" + strings.Join(tags, ",") + "}"
}

func arrayToTags(s string) []string {
	s = strings.Trim(s, "{}")
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

// makeSlug converts a title to a URL-friendly lowercase slug.
func makeSlug(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		if r == ' ' || r == '_' {
			return '-'
		}
		return -1
	}, s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if s == "" {
		s = "item"
	}
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}
