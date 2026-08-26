package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/paid-access/internal/httpapi"
	"github.com/fenghaoyun-monster/freedompost/services/paid-access/internal/ratelimit"
	"github.com/fenghaoyun-monster/freedompost/services/paid-access/internal/store"
	"github.com/fenghaoyun-monster/freedompost/services/paid-access/internal/turnstile"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel(os.Getenv("LOG_LEVEL"))}))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	database, err := store.Open(ctx, required("DATABASE_URL"))
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	limiter, err := ratelimit.NewRedis(required("REDIS_URL"))
	if err != nil {
		logger.Error("configure Redis limiter", "error", err)
		os.Exit(1)
	}
	defer limiter.Close()
	if err := limiter.Ping(ctx); err != nil {
		logger.Warn("Redis unavailable at startup; strict process-local fallback will be used", "error", err)
	}

	enabled := os.Getenv("PAID_ARTICLES_ENABLED") == "true"
	var turnstileVerifier httpapi.TurnstileVerifier = rejectingTurnstile{}
	turnstileSiteKey := "disabled"
	internalSecret := required("PAID_ACCESS_INTERNAL_SECRET")
	if enabled {
		turnstileVerifier, err = turnstile.New(turnstile.Config{
			SecretKey:        required("TURNSTILE_SECRET_KEY"),
			ExpectedHostname: required("TURNSTILE_EXPECTED_HOSTNAME"),
			HTTPClient:       &http.Client{Timeout: durationMilliseconds("TURNSTILE_TIMEOUT_MS", 3*time.Second)},
		})
		if err != nil {
			logger.Error("configure Turnstile", "error", err)
			os.Exit(1)
		}
		turnstileSiteKey = required("TURNSTILE_SITE_KEY")
	}

	handler, err := httpapi.New(httpapi.Config{
		Enabled:            enabled,
		Store:              database,
		Turnstile:          turnstileVerifier,
		Limiter:            limiter,
		TurnstileSiteKey:   turnstileSiteKey,
		CookieSecure:       os.Getenv("COOKIE_SECURE") != "false",
		PublicOrigin:       required("PUBLIC_SITE_URL"),
		TrustProxy:         os.Getenv("TRUST_PROXY") == "true",
		InternalSecret:     internalSecret,
		SupportWechatImage: valueOr("PAID_ACCESS_WECHAT_IMAGE_URL", "/images/contact-wechat.jpg"),
		Logger:             logger,
	})
	if err != nil {
		logger.Error("configure HTTP API", "error", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              valueOr("PAID_ACCESS_ADDR", ":8080"),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}

	go func() {
		logger.Info("paid access service listening", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}

type rejectingTurnstile struct{}

func (rejectingTurnstile) Verify(context.Context, string, string, string) error {
	return turnstile.ErrUnavailable
}

func required(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		slog.Error("required environment variable is missing", "name", name)
		os.Exit(1)
	}
	return value
}

func valueOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func durationMilliseconds(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value + "ms")
	if err != nil || duration < 100*time.Millisecond || duration > 10*time.Second {
		slog.Error("invalid millisecond duration", "name", name)
		os.Exit(1)
	}
	return duration
}

func logLevel(value string) slog.Level {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
