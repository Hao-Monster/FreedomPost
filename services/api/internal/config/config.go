package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration loaded from environment variables.
// All fields are validated at startup; the process exits if required fields
// are missing or malformed.
type Config struct {
	// HTTP server
	Port int
	Host string

	// Database
	DatabaseURL string

	// Redis
	RedisURL string

	// Authentication
	CookieSecret      string
	CookieSecure      bool
	AdminUsername     string
	AdminPasswordHash string // bcrypt hash, derived from ADMIN_PASSWORD at startup

	// Visitor tracking
	VisitorHashSalt string

	// Storage
	StorageDriver       string // "local", "oss", "r2"
	LocalStorageRoot    string
	PublicUploadBaseURL string

	// Aliyun OSS (optional)
	AliyunOSSRegion          string
	AliyunOSSBucket          string
	AliyunOSSAccessKeyID     string
	AliyunOSSAccessKeySecret string
	AliyunOSSEndpoint        string
	AliyunOSSPublicBaseURL   string
	AliyunOSSPrefix          string

	// Cloudflare R2 (optional)
	R2AccountID       string
	R2Bucket          string
	R2AccessKeyID     string
	R2SecretAccessKey string
	R2Endpoint        string
	R2PublicBaseURL   string
	R2Prefix          string

	// Upload limits
	UploadMaxBytes    int64
	APIBodyLimitBytes int64

	// Proxy
	TrustProxy bool

	// paid-access internal service
	PaidAccessInternalURL    string
	PaidAccessInternalSecret string
	PaidAccessWechatImageURL string

	// Turnstile (Cloudflare)
	TurnstileSiteKey          string
	TurnstileSecretKey        string
	TurnstileExpectedHostname string
	TurnstileExpectedAction   string
	TurnstileTimeoutMS        int

	// Opus8 (benefit campaigns)
	Opus8BaseURL   string
	Opus8KeyID     string
	Opus8Secret    string
	Opus8TimeoutMS int

	// Benefit
	BenefitClaimHMACSecret   string
	BenefitLinkEncryptionKey string
	BenefitNetworkDailyLimit int
	BenefitClaimMinuteLimit  int

	// View count buffer flush interval
	ViewBufferFlushInterval time.Duration

	// Origin for CORS validation
	PublicSiteURL  string
	OriginTestHost string

	// Public site
	PreviewDomain string
}

// Load reads and validates all configuration from environment variables.
func Load() (*Config, error) {
	var errs []string

	requireEnv := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			errs = append(errs, fmt.Sprintf("%s is required", key))
		}
		return v
	}

	optionalEnv := func(key, fallback string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return fallback
	}

	parseInt := func(key string, fallback int) int {
		v := os.Getenv(key)
		if v == "" {
			return fallback
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s must be an integer: %v", key, err))
			return fallback
		}
		return n
	}

	parseInt64 := func(key string, fallback int64) int64 {
		v := os.Getenv(key)
		if v == "" {
			return fallback
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s must be an integer: %v", key, err))
			return fallback
		}
		return n
	}

	parseBool := func(key string, fallback bool) bool {
		v := os.Getenv(key)
		if v == "" {
			return fallback
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s must be a boolean: %v", key, err))
			return fallback
		}
		return b
	}

	cfg := &Config{
		Port:            parseInt("PORT", 3001),
		Host:            optionalEnv("HOST", "0.0.0.0"),
		DatabaseURL:     requireEnv("DATABASE_URL"),
		RedisURL:        requireEnv("REDIS_URL"),
		CookieSecret:    requireEnv("COOKIE_SECRET"),
		CookieSecure:    parseBool("COOKIE_SECURE", false),
		AdminUsername:   optionalEnv("ADMIN_USERNAME", "admin"),
		VisitorHashSalt: requireEnv("VISITOR_HASH_SALT"),

		StorageDriver:       optionalEnv("STORAGE_DRIVER", "local"),
		LocalStorageRoot:    optionalEnv("LOCAL_STORAGE_ROOT", "runtime/local-storage"),
		PublicUploadBaseURL: optionalEnv("PUBLIC_UPLOAD_BASE_URL", "/api/uploads"),

		AliyunOSSRegion:          os.Getenv("ALIYUN_OSS_REGION"),
		AliyunOSSBucket:          os.Getenv("ALIYUN_OSS_BUCKET"),
		AliyunOSSAccessKeyID:     os.Getenv("ALIYUN_OSS_ACCESS_KEY_ID"),
		AliyunOSSAccessKeySecret: os.Getenv("ALIYUN_OSS_ACCESS_KEY_SECRET"),
		AliyunOSSEndpoint:        os.Getenv("ALIYUN_OSS_ENDPOINT"),
		AliyunOSSPublicBaseURL:   os.Getenv("ALIYUN_OSS_PUBLIC_BASE_URL"),
		AliyunOSSPrefix:          os.Getenv("ALIYUN_OSS_PREFIX"),

		R2AccountID:       os.Getenv("R2_ACCOUNT_ID"),
		R2Bucket:          optionalEnv("R2_BUCKET", "freedompost"),
		R2AccessKeyID:     os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2Endpoint:        os.Getenv("R2_ENDPOINT"),
		R2PublicBaseURL:   os.Getenv("R2_PUBLIC_BASE_URL"),
		R2Prefix:          optionalEnv("R2_PREFIX", "freedompost/uploads"),

		UploadMaxBytes:    parseInt64("UPLOAD_MAX_BYTES", 524288000),   // 500 MB
		APIBodyLimitBytes: parseInt64("API_BODY_LIMIT_BYTES", 16*1024), // 16 KB default for JSON

		TrustProxy: parseBool("TRUST_PROXY", false),

		PaidAccessInternalURL:    optionalEnv("PAID_ACCESS_INTERNAL_URL", "http://paid-access:8080"),
		PaidAccessInternalSecret: os.Getenv("PAID_ACCESS_INTERNAL_SECRET"),
		PaidAccessWechatImageURL: os.Getenv("PAID_ACCESS_WECHAT_IMAGE_URL"),

		TurnstileSiteKey:          os.Getenv("TURNSTILE_SITE_KEY"),
		TurnstileSecretKey:        os.Getenv("TURNSTILE_SECRET_KEY"),
		TurnstileExpectedHostname: os.Getenv("TURNSTILE_EXPECTED_HOSTNAME"),
		TurnstileExpectedAction:   optionalEnv("TURNSTILE_EXPECTED_ACTION", "webmaster_benefit_claim"),
		TurnstileTimeoutMS:        parseInt("TURNSTILE_TIMEOUT_MS", 3000),

		Opus8BaseURL:   os.Getenv("OPUS8_INTEGRATION_BASE_URL"),
		Opus8KeyID:     os.Getenv("OPUS8_INTEGRATION_KEY_ID"),
		Opus8Secret:    os.Getenv("OPUS8_INTEGRATION_SECRET"),
		Opus8TimeoutMS: parseInt("OPUS8_INTEGRATION_TIMEOUT_MS", 5000),

		BenefitClaimHMACSecret:   requireEnv("BENEFIT_CLAIM_HMAC_SECRET"),
		BenefitLinkEncryptionKey: requireEnv("BENEFIT_LINK_ENCRYPTION_KEY"),
		BenefitNetworkDailyLimit: parseInt("BENEFIT_NETWORK_DAILY_LIMIT", 3),
		BenefitClaimMinuteLimit:  parseInt("BENEFIT_CLAIM_MINUTE_LIMIT", 6),

		ViewBufferFlushInterval: 30 * time.Second,

		PublicSiteURL:  os.Getenv("PUBLIC_SITE_URL"),
		OriginTestHost: os.Getenv("ORIGIN_TEST_HOST"),
		PreviewDomain:  os.Getenv("PREVIEW_DOMAIN"),
	}

	// Validate storage driver
	switch cfg.StorageDriver {
	case "local", "oss", "r2":
	default:
		errs = append(errs, fmt.Sprintf("STORAGE_DRIVER must be 'local', 'oss', or 'r2', got %q", cfg.StorageDriver))
	}

	if cfg.StorageDriver == "oss" {
		if cfg.AliyunOSSRegion == "" {
			errs = append(errs, "ALIYUN_OSS_REGION is required when STORAGE_DRIVER=oss")
		}
		if cfg.AliyunOSSBucket == "" {
			errs = append(errs, "ALIYUN_OSS_BUCKET is required when STORAGE_DRIVER=oss")
		}
		if cfg.AliyunOSSAccessKeyID == "" {
			errs = append(errs, "ALIYUN_OSS_ACCESS_KEY_ID is required when STORAGE_DRIVER=oss")
		}
		if cfg.AliyunOSSAccessKeySecret == "" {
			errs = append(errs, "ALIYUN_OSS_ACCESS_KEY_SECRET is required when STORAGE_DRIVER=oss")
		}
	}

	if cfg.StorageDriver == "r2" {
		if cfg.R2AccountID == "" {
			errs = append(errs, "R2_ACCOUNT_ID is required when STORAGE_DRIVER=r2")
		}
		if cfg.R2AccessKeyID == "" {
			errs = append(errs, "R2_ACCESS_KEY_ID is required when STORAGE_DRIVER=r2")
		}
		if cfg.R2SecretAccessKey == "" {
			errs = append(errs, "R2_SECRET_ACCESS_KEY is required when STORAGE_DRIVER=r2")
		}
	}

	// Parse admin password.
	// Priority: ADMIN_PASSWORD_HASH (bcrypt) > ADMIN_PASSWORD (plaintext, dev only)
	adminPasswordHash := os.Getenv("ADMIN_PASSWORD_HASH")
	adminPassword := os.Getenv("ADMIN_PASSWORD")
	switch {
	case adminPasswordHash != "":
		cfg.AdminPasswordHash = adminPasswordHash // production: pre-hashed
	case adminPassword != "":
		cfg.AdminPasswordHash = adminPassword // dev: stored as plaintext, compared with constant-time
	default:
		errs = append(errs, "ADMIN_PASSWORD or ADMIN_PASSWORD_HASH is required")
	}

	// Validate PublicSiteURL
	if cfg.PublicSiteURL != "" {
		if _, err := url.Parse(cfg.PublicSiteURL); err != nil {
			errs = append(errs, fmt.Sprintf("PUBLIC_SITE_URL is invalid: %v", err))
		}
	}

	if len(errs) > 0 {
		return nil, errors.New("configuration errors:\n  " + strings.Join(errs, "\n  "))
	}

	return cfg, nil
}

// Addr returns the listen address in "host:port" format.
func (c *Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// AllowedOrigins returns the set of allowed CORS origins based on config.
func (c *Config) AllowedOrigins() map[string]bool {
	origins := make(map[string]bool)
	if c.PublicSiteURL != "" {
		// Normalize: strip trailing slash, lowercase scheme+host
		if u, err := url.Parse(c.PublicSiteURL); err == nil {
			origins[u.Scheme+"://"+u.Host] = true
		}
	}
	if c.OriginTestHost != "" {
		origins["http://"+c.OriginTestHost] = true
		origins["https://"+c.OriginTestHost] = true
	}
	if c.PreviewDomain != "" {
		origins["https://"+c.PreviewDomain] = true
	}
	return origins
}
