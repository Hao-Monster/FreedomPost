// Package session provides Redis-backed session storage for admin and
// affiliate sessions, replacing the in-memory Map used by the TypeScript API.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
)

const (
	AdminSessionTTL     = 24 * time.Hour
	AffiliateSessionTTL = 30 * 24 * time.Hour

	adminPrefix     = "fp:session:admin:"
	affiliatePrefix = "fp:session:affiliate:"
)

// Store manages sessions in Redis.
type Store struct {
	client *redis.Client
}

// NewStore creates a session store backed by Redis.
func NewStore(redisURL string) (*Store, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis URL for session store: %w", err)
	}
	return &Store{client: redis.NewClient(opts)}, nil
}

// ─── Admin sessions ───────────────────────────────────────────────────────────

// SetAdminSession stores an admin session in Redis with a sliding TTL.
// key should be SHA256(rawToken) — never store raw tokens.
func (s *Store) SetAdminSession(ctx context.Context, tokenHash string, session domain.AdminSession) error {
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal admin session: %w", err)
	}
	return s.client.Set(ctx, adminPrefix+tokenHash, data, AdminSessionTTL).Err()
}

// GetAdminSession retrieves and slides the TTL of an admin session.
func (s *Store) GetAdminSession(ctx context.Context, tokenHash string) (*domain.AdminSession, error) {
	key := adminPrefix + tokenHash
	data, err := s.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get admin session: %w", err)
	}
	var session domain.AdminSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("unmarshal admin session: %w", err)
	}
	// Slide TTL
	_ = s.client.Expire(ctx, key, AdminSessionTTL)
	return &session, nil
}

// DeleteAdminSession revokes an admin session.
func (s *Store) DeleteAdminSession(ctx context.Context, tokenHash string) error {
	return s.client.Del(ctx, adminPrefix+tokenHash).Err()
}

// ─── Affiliate sessions ───────────────────────────────────────────────────────

// SetAffiliateSession stores an affiliate session with a sliding TTL.
func (s *Store) SetAffiliateSession(ctx context.Context, tokenHash string, session domain.AffiliateSession) error {
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal affiliate session: %w", err)
	}
	return s.client.Set(ctx, affiliatePrefix+tokenHash, data, AffiliateSessionTTL).Err()
}

// GetAffiliateSession retrieves and slides the TTL of an affiliate session.
func (s *Store) GetAffiliateSession(ctx context.Context, tokenHash string) (*domain.AffiliateSession, error) {
	key := affiliatePrefix + tokenHash
	data, err := s.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get affiliate session: %w", err)
	}
	var session domain.AffiliateSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("unmarshal affiliate session: %w", err)
	}
	// Slide TTL
	_ = s.client.Expire(ctx, key, AffiliateSessionTTL)
	return &session, nil
}

// DeleteAffiliateSession revokes an affiliate session.
func (s *Store) DeleteAffiliateSession(ctx context.Context, tokenHash string) error {
	return s.client.Del(ctx, affiliatePrefix+tokenHash).Err()
}

// ─── Health ───────────────────────────────────────────────────────────────────

func (s *Store) Ping(ctx context.Context) error { return s.client.Ping(ctx).Err() }
func (s *Store) Close() error                   { return s.client.Close() }
