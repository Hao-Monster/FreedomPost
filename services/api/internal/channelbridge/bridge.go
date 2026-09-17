// Package channelbridge routes verified Chatwoot messages through a configured
// external customer-service channel without coupling order creation to it.
package channelbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/wecom"
)

const (
	conversationReferencePrefix = "FP-CW:"
	mappingTTL                  = 30 * 24 * time.Hour
	deliveryTTL                 = 24 * time.Hour
)

var conversationReference = regexp.MustCompile(`^\s*FP-CW:(\d+)\s+(.+?)\s*$`)

// WebhookEvent is the minimal, untrusted Chatwoot event shape consumed by the
// bridge. Unknown fields are ignored so Chatwoot can add fields compatibly.
type WebhookEvent struct {
	Event        string `json:"event"`
	ID           int64  `json:"id"`
	MessageType  string `json:"message_type"`
	Content      string `json:"content"`
	Conversation struct {
		ID int64 `json:"id"`
	} `json:"conversation"`
	Account struct {
		ID int64 `json:"id"`
	} `json:"account"`
}

// Deduper prevents replayed webhook deliveries from being forwarded twice.
type Deduper interface {
	Claim(context.Context, string, time.Duration) (bool, error)
}

// DedupReleaser allows a failed downstream delivery to relinquish its claim
// immediately, so the provider retry can safely attempt the message again.
// Implementations that cannot release may still satisfy Deduper, but should
// understand that a failed send can remain claimed until its TTL expires.
type DedupReleaser interface {
	Release(context.Context, string) error
}

// ConversationStore persists a relationship for future routing improvements.
type ConversationStore interface {
	Put(context.Context, string, int64, time.Duration) error
	Latest(context.Context, string) (int64, error)
}

// Service routes messages between Chatwoot and the configured WeCom operator.
// It fails closed when a message has no explicit conversation reference.
type Service struct {
	chatwoot               *ChatwootClient
	wecom                  *wecom.Client
	operatorUserID         string
	webhookSecret          string
	deduper                Deduper
	mappings               ConversationStore
	allowUnprefixedReplies bool
	now                    func() time.Time
}

type ServiceConfig struct {
	Chatwoot               *ChatwootClient
	WeCom                  *wecom.Client
	OperatorUserID         string
	WebhookSecret          string
	Deduper                Deduper
	Mappings               ConversationStore
	AllowUnprefixedReplies bool
}

// WebhookSecret returns the configured Chatwoot signing secret for the HTTP
// adapter. The caller must never log or expose this value.
func (s *Service) WebhookSecret() string { return s.webhookSecret }

// WeComClient returns the configured WeCom client for callback verification.
// The client does not expose credentials or mutable token state.
func (s *Service) WeComClient() *wecom.Client { return s.wecom }

func NewService(cfg ServiceConfig) (*Service, error) {
	if cfg.Chatwoot == nil || cfg.WeCom == nil {
		return nil, errors.New("chatwoot and wecom clients are required")
	}
	if strings.TrimSpace(cfg.OperatorUserID) == "" {
		return nil, errors.New("wecom operator user ID is required")
	}
	if strings.TrimSpace(cfg.WebhookSecret) == "" {
		return nil, errors.New("chatwoot webhook secret is required")
	}
	return &Service{
		chatwoot:               cfg.Chatwoot,
		wecom:                  cfg.WeCom,
		operatorUserID:         strings.TrimSpace(cfg.OperatorUserID),
		webhookSecret:          strings.TrimSpace(cfg.WebhookSecret),
		deduper:                cfg.Deduper,
		mappings:               cfg.Mappings,
		allowUnprefixedReplies: cfg.AllowUnprefixedReplies,
		now:                    time.Now,
	}, nil
}

// HandleChatwootWebhook verifies a delivery and forwards incoming visitor
// messages to the configured WeCom operator.
func (s *Service) HandleChatwootWebhook(ctx context.Context, rawBody, timestamp, signature string) error {
	if !VerifyWebhookSignature(rawBody, timestamp, signature, s.webhookSecret, s.now(), 5*time.Minute) {
		return errors.New("chatwoot webhook signature rejected")
	}
	var event WebhookEvent
	if err := json.Unmarshal([]byte(rawBody), &event); err != nil {
		return errors.New("chatwoot webhook JSON is invalid")
	}
	if event.Event != "message_created" || event.MessageType != "incoming" {
		return nil
	}
	if event.Conversation.ID <= 0 {
		return errors.New("chatwoot webhook conversation is invalid")
	}
	if event.Account.ID != 0 && event.Account.ID != s.chatwoot.accountID {
		return errors.New("chatwoot webhook account mismatch")
	}
	deliveryKey := ""
	if event.ID > 0 && s.deduper != nil {
		deliveryKey = fmt.Sprintf("chatwoot:delivery:%d", event.ID)
		claimed, err := s.deduper.Claim(ctx, deliveryKey, deliveryTTL)
		if err != nil {
			return errors.New("chatwoot webhook deduplication failed")
		}
		if !claimed {
			return nil
		}
	}
	content := strings.TrimSpace(event.Content)
	if content == "" {
		return nil
	}
	forwarded := fmt.Sprintf("%s%d\n%s", conversationReferencePrefix, event.Conversation.ID, content)
	if err := s.wecom.SendText(ctx, s.operatorUserID, forwarded); err != nil {
		if deliveryKey != "" {
			s.releaseClaim(ctx, deliveryKey)
		}
		return err
	}
	if s.mappings != nil {
		if err := s.mappings.Put(ctx, fmt.Sprintf("wecom:%s:%d", s.operatorUserID, event.Conversation.ID), event.Conversation.ID, mappingTTL); err != nil {
			return errors.New("conversation mapping could not be stored")
		}
	}
	return nil
}

// HandleWeComMessage accepts only messages from the configured operator and
// requires the visible FP-CW:<conversation_id> prefix to prevent cross-user
// replies. The prefix is removed before adding the outgoing Chatwoot message.
func (s *Service) HandleWeComMessage(ctx context.Context, message wecom.Message) error {
	if strings.TrimSpace(message.FromUserName) != s.operatorUserID {
		return errors.New("wecom sender is not the configured operator")
	}
	if message.MsgType != "text" {
		return nil
	}
	match := conversationReference.FindStringSubmatch(message.Content)
	content := strings.TrimSpace(message.Content)
	conversationID := int64(0)
	if len(match) == 3 {
		var err error
		conversationID, err = strconv.ParseInt(match[1], 10, 64)
		if err != nil || conversationID <= 0 {
			return errors.New("wecom conversation reference is invalid")
		}
		content = strings.TrimSpace(match[2])
	} else if s.allowUnprefixedReplies && s.mappings != nil {
		var err error
		conversationID, err = s.mappings.Latest(ctx, "wecom:"+s.operatorUserID)
		if err != nil || conversationID <= 0 {
			return errors.New("wecom reply has no active conversation")
		}
	} else {
		return errors.New("wecom reply must include FP-CW:<conversation_id>")
	}
	deliveryKey := ""
	if s.deduper != nil && message.MsgID != "" {
		deliveryKey = "wecom:delivery:" + message.MsgID
		claimed, err := s.deduper.Claim(ctx, deliveryKey, deliveryTTL)
		if err != nil {
			return errors.New("wecom message deduplication failed")
		}
		if !claimed {
			return nil
		}
	}
	if err := s.chatwoot.SendOutgoingMessage(ctx, conversationID, content); err != nil {
		if deliveryKey != "" {
			s.releaseClaim(ctx, deliveryKey)
		}
		return err
	}
	return nil
}

func (s *Service) releaseClaim(ctx context.Context, key string) {
	if releaser, ok := s.deduper.(DedupReleaser); ok {
		_ = releaser.Release(ctx, key)
	}
}

// RedisDeduper and RedisConversationStore use the existing authenticated Redis
// service. Values contain no message body or credential.
type RedisDeduper struct {
	Client *redis.Client
	Prefix string
}

func (d RedisDeduper) Claim(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if d.Client == nil {
		return false, errors.New("redis client is nil")
	}
	prefix := d.Prefix
	if prefix == "" {
		prefix = "fp:channel:"
	}
	return d.Client.SetNX(ctx, prefix+key, "1", ttl).Result()
}

func (d RedisDeduper) Release(ctx context.Context, key string) error {
	if d.Client == nil {
		return errors.New("redis client is nil")
	}
	prefix := d.Prefix
	if prefix == "" {
		prefix = "fp:channel:"
	}
	return d.Client.Del(ctx, prefix+key).Err()
}

type RedisConversationStore struct {
	Client *redis.Client
	Prefix string
}

func (s RedisConversationStore) Put(ctx context.Context, key string, conversationID int64, ttl time.Duration) error {
	if s.Client == nil {
		return errors.New("redis client is nil")
	}
	prefix := s.Prefix
	if prefix == "" {
		prefix = "fp:channel:mapping:"
	}
	if err := s.Client.Set(ctx, prefix+key, strconv.FormatInt(conversationID, 10), ttl).Err(); err != nil {
		return err
	}
	parts := strings.SplitN(key, ":", 3)
	if len(parts) >= 2 {
		return s.Client.Set(ctx, prefix+parts[0]+":"+parts[1], strconv.FormatInt(conversationID, 10), ttl).Err()
	}
	return nil
}

func (s RedisConversationStore) Latest(ctx context.Context, key string) (int64, error) {
	prefix := s.Prefix
	if prefix == "" {
		prefix = "fp:channel:mapping:"
	}
	value, err := s.Client.Get(ctx, prefix+key).Result()
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(value, 10, 64)
}
