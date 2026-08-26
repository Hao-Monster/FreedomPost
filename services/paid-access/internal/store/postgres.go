package store

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/paid-access/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, databaseURL string) (*Postgres, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	config.MaxConns = 12
	config.MinConns = 1
	config.MaxConnLifetime = 30 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (store *Postgres) Close() { store.pool.Close() }

func (store *Postgres) CreateAccount(ctx context.Context, loginName, normalizedLogin, passwordHash string) (domain.Account, error) {
	var account domain.Account
	err := store.pool.QueryRow(ctx, `
		INSERT INTO reader_accounts (login_name, normalized_login, password_hash)
		VALUES ($1, $2, $3)
		RETURNING id, login_name, normalized_login, password_hash, credential_version, status, created_at`,
		loginName, normalizedLogin, passwordHash,
	).Scan(&account.ID, &account.LoginName, &account.NormalizedLogin, &account.PasswordHash, &account.CredentialVersion, &account.Status, &account.CreatedAt)
	if isUniqueViolation(err) {
		return domain.Account{}, domain.ErrConflict
	}
	return account, mapError(err)
}

func (store *Postgres) FindAccountByLogin(ctx context.Context, normalizedLogin string) (domain.Account, error) {
	var account domain.Account
	err := store.pool.QueryRow(ctx, `
		SELECT id, login_name, normalized_login, password_hash, credential_version, status, created_at
		FROM reader_accounts WHERE normalized_login = $1`, normalizedLogin,
	).Scan(&account.ID, &account.LoginName, &account.NormalizedLogin, &account.PasswordHash, &account.CredentialVersion, &account.Status, &account.CreatedAt)
	return account, mapError(err)
}

func (store *Postgres) CreateSession(ctx context.Context, accountID, tokenHash string, credentialVersion int, metadata domain.SessionMetadata) error {
	_, err := store.pool.Exec(ctx, `
		INSERT INTO reader_sessions (account_id, token_hash, credential_version, user_agent_hash, ip_hash)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''))`, accountID, tokenHash, credentialVersion, metadata.UserAgentHash, metadata.IPHash)
	return err
}

func (store *Postgres) FindAccountBySession(ctx context.Context, tokenHash string) (domain.Account, error) {
	var account domain.Account
	err := store.pool.QueryRow(ctx, `
		SELECT a.id, a.login_name, a.normalized_login, a.password_hash, a.credential_version, a.status, a.created_at
		FROM reader_sessions s
		JOIN reader_accounts a ON a.id = s.account_id
		WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND a.status = 'active'
		  AND s.credential_version = a.credential_version
		  AND s.last_seen_at > NOW() - INTERVAL '30 days'`, tokenHash,
	).Scan(&account.ID, &account.LoginName, &account.NormalizedLogin, &account.PasswordHash, &account.CredentialVersion, &account.Status, &account.CreatedAt)
	return account, mapError(err)
}

func (store *Postgres) TouchSession(ctx context.Context, tokenHash string) error {
	_, err := store.pool.Exec(ctx, `UPDATE reader_sessions SET last_seen_at = NOW() WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash)
	return err
}

func (store *Postgres) RevokeSession(ctx context.Context, tokenHash string) error {
	_, err := store.pool.Exec(ctx, `UPDATE reader_sessions SET revoked_at = COALESCE(revoked_at, NOW()) WHERE token_hash = $1`, tokenHash)
	return err
}

func (store *Postgres) RevokeAllSessions(ctx context.Context, accountID string) error {
	_, err := store.pool.Exec(ctx, `UPDATE reader_sessions SET revoked_at = COALESCE(revoked_at, NOW()) WHERE account_id = $1`, accountID)
	return err
}

func (store *Postgres) FindArticle(ctx context.Context, slug string) (domain.Article, error) {
	var article domain.Article
	err := store.pool.QueryRow(ctx, `
		SELECT id, slug, title, COALESCE(excerpt, ''), content_html, visibility, price_cents, currency,
		       created_at, updated_at, view_count, comment_count, attachment_count
		FROM posts WHERE slug = $1`, slug,
	).Scan(&article.ID, &article.Slug, &article.Title, &article.Excerpt, &article.ContentHTML, &article.Visibility,
		&article.PriceCents, &article.Currency, &article.CreatedAt, &article.UpdatedAt, &article.ViewCount, &article.CommentCount, &article.AttachmentCount)
	return article, mapError(err)
}

func (store *Postgres) HasEntitlement(ctx context.Context, accountID, postID string) (bool, error) {
	var entitled bool
	err := store.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM article_entitlements WHERE account_id = $1 AND post_id = $2)`, accountID, postID).Scan(&entitled)
	return entitled, err
}

func (store *Postgres) CreateOrder(ctx context.Context, accountID string, article domain.Article) (domain.Order, bool, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return domain.Order{}, false, err
	}
	defer tx.Rollback(ctx)

	// Serialize order creation for one reader/article pair. The database unique
	// constraint remains the final invariant, while this lock lets concurrent
	// retries consistently receive the existing pending order instead of a 500.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, accountID+":"+article.ID); err != nil {
		return domain.Order{}, false, err
	}

	var current domain.Article
	err = tx.QueryRow(ctx, `SELECT id, slug, title, visibility, price_cents, currency FROM posts WHERE id = $1 FOR SHARE`, article.ID).
		Scan(&current.ID, &current.Slug, &current.Title, &current.Visibility, &current.PriceCents, &current.Currency)
	if err != nil {
		return domain.Order{}, false, mapError(err)
	}
	if current.Visibility != "paid" || current.PriceCents <= 0 {
		return domain.Order{}, false, domain.ErrInvalidState
	}
	var entitled bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM article_entitlements WHERE account_id = $1 AND post_id = $2)`, accountID, current.ID).Scan(&entitled); err != nil {
		return domain.Order{}, false, err
	}
	if entitled {
		return domain.Order{}, false, domain.ErrAlreadyEntitled
	}
	if existing, findErr := scanOrder(tx.QueryRow(ctx, orderSelect+` WHERE o.account_id = $1 AND o.post_id = $2 AND o.status = 'pending' FOR UPDATE OF o`, accountID, current.ID)); findErr == nil {
		if err := tx.Commit(ctx); err != nil {
			return domain.Order{}, false, err
		}
		return existing, false, nil
	} else if !errors.Is(findErr, domain.ErrNotFound) {
		return domain.Order{}, false, findErr
	}

	orderCode, err := newOrderCode()
	if err != nil {
		return domain.Order{}, false, err
	}
	var order domain.Order
	err = tx.QueryRow(ctx, `
		INSERT INTO article_orders (order_code, account_id, post_id, post_title, price_cents, currency)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, order_code, account_id, post_id, $7, post_title, price_cents, currency, status, created_at, updated_at, completed_at`,
		orderCode, accountID, current.ID, current.Title, current.PriceCents, current.Currency, current.Slug,
	).Scan(&order.ID, &order.OrderCode, &order.AccountID, &order.PostID, &order.PostSlug, &order.PostTitle,
		&order.PriceCents, &order.Currency, &order.Status, &order.CreatedAt, &order.UpdatedAt, &order.CompletedAt)
	if isUniqueViolation(err) {
		return domain.Order{}, false, domain.ErrConflict
	}
	if err != nil {
		return domain.Order{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Order{}, false, err
	}
	return order, true, nil
}

const orderSelect = `
	SELECT o.id, o.order_code, o.account_id, a.login_name, o.post_id, p.slug, o.post_title,
	       o.price_cents, o.currency, o.status, o.created_at, o.updated_at, o.completed_at
	FROM article_orders o
	JOIN reader_accounts a ON a.id = o.account_id
	JOIN posts p ON p.id = o.post_id`

func (store *Postgres) ListAccountOrders(ctx context.Context, accountID string) ([]domain.Order, error) {
	rows, err := store.pool.Query(ctx, orderSelect+` WHERE o.account_id = $1 ORDER BY o.created_at DESC LIMIT 100`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectOrders(rows)
}

func (store *Postgres) ListAdminOrders(ctx context.Context) ([]domain.Order, error) {
	rows, err := store.pool.Query(ctx, orderSelect+` ORDER BY o.created_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectOrders(rows)
}

func (store *Postgres) UpdateOrderStatus(ctx context.Context, orderID, status, actor string) (domain.Order, error) {
	if status != "pending" && status != "completed" && status != "canceled" {
		return domain.Order{}, domain.ErrInvalidState
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return domain.Order{}, err
	}
	defer tx.Rollback(ctx)

	var current, accountID, postID string
	if err := tx.QueryRow(ctx, `SELECT status, account_id, post_id FROM article_orders WHERE id = $1 FOR UPDATE`, orderID).Scan(&current, &accountID, &postID); err != nil {
		return domain.Order{}, mapError(err)
	}
	if current == "completed" && status != "completed" {
		return domain.Order{}, domain.ErrInvalidState
	}
	_, err = tx.Exec(ctx, `
		UPDATE article_orders SET status = $2::varchar, updated_at = NOW(),
		completed_at = CASE WHEN $2::varchar = 'completed' THEN COALESCE(completed_at, NOW()) ELSE NULL END,
		completed_by = CASE WHEN $2::varchar = 'completed' THEN $3::text ELSE NULL END
		WHERE id = $1`, orderID, status, actor)
	if err != nil {
		return domain.Order{}, err
	}
	if status == "completed" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO article_entitlements (account_id, post_id, source_order_id, granted_by)
			VALUES ($1, $2, $3, $4) ON CONFLICT (account_id, post_id) DO NOTHING`, accountID, postID, orderID, actor); err != nil {
			return domain.Order{}, err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO article_access_audit_log (actor_type, actor_id, action, target_type, target_id, metadata)
		VALUES ('admin', $1, 'article_order_status_changed', 'article_order', $2, jsonb_build_object('from', $3::text, 'to', $4::text))`, actor, orderID, current, status); err != nil {
		return domain.Order{}, err
	}
	order, err := scanOrder(tx.QueryRow(ctx, orderSelect+` WHERE o.id = $1`, orderID))
	if err != nil {
		return domain.Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Order{}, err
	}
	return order, nil
}

func (store *Postgres) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	rows, err := store.pool.Query(ctx, `SELECT id, login_name, normalized_login, password_hash, credential_version, status, created_at FROM reader_accounts ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := make([]domain.Account, 0)
	for rows.Next() {
		var account domain.Account
		if err := rows.Scan(&account.ID, &account.LoginName, &account.NormalizedLogin, &account.PasswordHash, &account.CredentialVersion, &account.Status, &account.CreatedAt); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (store *Postgres) ResetPassword(ctx context.Context, accountID, passwordHash, actor string) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE reader_accounts SET password_hash = $2, credential_version = credential_version + 1, updated_at = NOW() WHERE id = $1`, accountID, passwordHash)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE reader_sessions SET revoked_at = COALESCE(revoked_at, NOW()) WHERE account_id = $1`, accountID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO article_access_audit_log (actor_type, actor_id, action, target_type, target_id) VALUES ('admin', $1, 'reader_password_reset', 'reader_account', $2)`, actor, accountID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type rowScanner interface{ Scan(...any) error }

func scanOrder(row rowScanner) (domain.Order, error) {
	var order domain.Order
	err := row.Scan(&order.ID, &order.OrderCode, &order.AccountID, &order.LoginName, &order.PostID, &order.PostSlug,
		&order.PostTitle, &order.PriceCents, &order.Currency, &order.Status, &order.CreatedAt, &order.UpdatedAt, &order.CompletedAt)
	return order, mapError(err)
}

func collectOrders(rows pgx.Rows) ([]domain.Order, error) {
	orders := make([]domain.Order, 0)
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, rows.Err()
}

func newOrderCode() (string, error) {
	raw := make([]byte, 7)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "FP" + strings.TrimRight(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), "="), nil
}

func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}

func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}
