// Command server is the entry point for the FreedomPost Go API service.
// It replaces apps/api (TypeScript) with a high-performance, type-safe Go implementation.
//
// Flags:
//
//	-hash-password <pw>  Print bcrypt hash of password and exit (use for ADMIN_PASSWORD_HASH)
//	-health-check        Perform HTTP health check against running server and exit
//	-migrate             Apply pending SQL migrations from MIGRATIONS_DIR (default /migrations) and exit
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/redis/go-redis/v9"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/benefit"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/config"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/httpapi"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/migrate"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/paidaccess"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/ratelimit"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/repository"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/searchindex"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/session"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/viewcount"
)

// version is injected at build time via -ldflags "-X main.version=..."
var version = "dev"

func main() {
	// ── CLI flags ──────────────────────────────────────────────────────────────
	hashPw := flag.String("hash-password", "", "Print bcrypt hash of given password and exit")
	healthCheck := flag.Bool("health-check", false, "HTTP health check against running server and exit")
	runMigrate := flag.Bool("migrate", false, "Apply pending SQL migrations from MIGRATIONS_DIR and exit")

	// If container ENTRYPOINT ["/fp-api"] is invoked with arguments like "/fp-api -migrate",
	// os.Args will be ["/fp-api", "/fp-api", "-migrate"]. Strip leading binary name if passed as an argument.
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "/fp-api" || args[0] == "fp-api" || args[0] == "./fp-api" || args[0] == "fp-api.exe") {
		args = args[1:]
	}
	if err := flag.CommandLine.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "flag parse error: %v\n", err)
		os.Exit(2)
	}

	// Mode: hash-password
	if *hashPw != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(*hashPw), 12)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hash error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(hash))
		return
	}

	// Mode: health-check (used by Docker HEALTHCHECK)
	if *healthCheck {
		port := os.Getenv("PORT")
		if port == "" {
			port = "3001"
		}
		resp, err := http.Get("http://127.0.0.1:" + port + "/health")
		if err != nil {
			fmt.Fprintln(os.Stderr, "health check failed:", err)
			os.Exit(1)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}

	// Mode: migrate (apply pending SQL migrations and exit)
	if *runMigrate {
		// Set up a logger that writes to stdout (not stderr) so deploy scripts
		// don't mistake log output for error output.
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
		migrationsDir := os.Getenv("MIGRATIONS_DIR")
		if migrationsDir == "" {
			migrationsDir = "/migrations"
		}
		databaseURL := os.Getenv("DATABASE_URL")
		if databaseURL == "" {
			fmt.Fprintln(os.Stderr, "migrate: DATABASE_URL is required")
			os.Exit(1)
		}
		if err := migrate.Run(context.Background(), databaseURL, migrationsDir); err != nil {
			fmt.Fprintln(os.Stderr, "migrate:", err)
			os.Exit(1)
		}
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// ── 1. Load & validate configuration ──────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger.Info("config loaded",
		"port", cfg.Port,
		"storage_driver", cfg.StorageDriver,
		"version", version,
	)

	// ── 2. PostgreSQL connection pool ─────────────────────────────────────────
	repo, err := repository.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer repo.Close()
	logger.Info("postgres connected")

	// ── 3. Redis (shared connection for rate limiter and sessions) ────────────
	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return err
	}
	redisClient := redis.NewClient(redisOpts)
	defer redisClient.Close()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.Warn("redis ping failed — rate limiting and sessions will degrade", "error", err)
	} else {
		logger.Info("redis connected")
	}

	// ── 4. Session store ──────────────────────────────────────────────────────
	sessions, err := session.NewStore(cfg.RedisURL)
	if err != nil {
		return err
	}
	defer sessions.Close()

	// ── 5. Rate limiter ───────────────────────────────────────────────────────
	limiter, err := ratelimit.NewRedis(cfg.RedisURL, "fp:api:")
	if err != nil {
		return err
	}
	defer limiter.Close()

	// ── 6. Storage adapter ────────────────────────────────────────────────────
	var stor storage.Adapter
	var localStore *storage.LocalAdapter

	switch cfg.StorageDriver {
	case "local":
		la, err := storage.NewLocalAdapter(cfg.LocalStorageRoot, cfg.PublicUploadBaseURL)
		if err != nil {
			return err
		}
		stor = la
		localStore = la
		logger.Info("storage: local", "root", cfg.LocalStorageRoot)

	case "oss":
		sa, err := storage.NewOSSAdapter(
			cfg.AliyunOSSRegion, cfg.AliyunOSSBucket,
			cfg.AliyunOSSAccessKeyID, cfg.AliyunOSSAccessKeySecret,
			cfg.AliyunOSSEndpoint, cfg.AliyunOSSPublicBaseURL, cfg.AliyunOSSPrefix,
		)
		if err != nil {
			return err
		}
		stor = sa
		logger.Info("storage: oss", "bucket", cfg.AliyunOSSBucket)

	case "r2":
		ra, err := storage.NewR2Adapter(
			cfg.R2AccountID, cfg.R2Bucket,
			cfg.R2AccessKeyID, cfg.R2SecretAccessKey,
			cfg.R2Endpoint, cfg.R2PublicBaseURL, cfg.R2Prefix,
		)
		if err != nil {
			return err
		}
		stor = ra
		logger.Info("storage: r2", "bucket", cfg.R2Bucket)

	default:
		return errors.New("unknown storage driver: " + cfg.StorageDriver)
	}

	// ── 7. View count buffer (async write to DB) ──────────────────────────────
	viewBuf := viewcount.NewBuffer(redisClient, repo, logger)
	go viewBuf.StartFlusher(ctx, cfg.ViewBufferFlushInterval)

	// ── 8. Search index cache ─────────────────────────────────────────────────
	searchCache := searchindex.NewCache()

	// ── 9. paid-access client ─────────────────────────────────────────────────
	paidAccessClient := paidaccess.NewClient(cfg.PaidAccessInternalURL, cfg.PaidAccessInternalSecret)

	// ── 10. Benefit runtime (optional — requires BENEFIT_* env vars) ──────────
	var benefitRuntime *httpapi.BenefitRuntime
	if cfg.BenefitClaimHMACSecret != "" {
		credSvc, bErr := benefit.NewClaimCredentialService(cfg.BenefitClaimHMACSecret)
		cipher, cErr := benefit.NewSubscriptionCipher(cfg.BenefitLinkEncryptionKey)
		if bErr != nil || cErr != nil {
			err := bErr
			if err == nil {
				err = cErr
			}
			logger.Error("benefit: config error", "error", err)
			return err
		}

		// Turnstile is optional in dev (skip if secret key not set)
		var turnstileVerifier *benefit.TurnstileVerifier
		if cfg.TurnstileSecretKey != "" {
			turnstileVerifier, err = benefit.NewTurnstileVerifier(
				cfg.TurnstileSecretKey,
				cfg.TurnstileExpectedHostname,
				cfg.TurnstileExpectedAction,
				cfg.TurnstileTimeoutMS,
			)
			if err != nil {
				logger.Error("benefit: turnstile config error", "error", err)
				return err
			}
		}

		// Opus8 is optional in dev (skip if base URL not set)
		var opus8Client *benefit.Opus8Client
		if cfg.Opus8BaseURL != "" {
			opus8Client, err = benefit.NewOpus8Client(
				cfg.Opus8BaseURL,
				cfg.Opus8KeyID,
				cfg.Opus8Secret,
				cfg.Opus8TimeoutMS,
			)
			if err != nil {
				logger.Error("benefit: opus8 config error", "error", err)
				return err
			}
		}

		benfitNetworkDailyLimit := 3
		if cfg.BenefitNetworkDailyLimit > 0 {
			benfitNetworkDailyLimit = cfg.BenefitNetworkDailyLimit
		}
		benfitClaimMinuteLimit := 6
		if cfg.BenefitClaimMinuteLimit > 0 {
			benfitClaimMinuteLimit = cfg.BenefitClaimMinuteLimit
		}

		benefitRuntime = &httpapi.BenefitRuntime{
			Credential:        credSvc,
			Cipher:            cipher,
			Turnstile:         turnstileVerifier,
			Opus8:             opus8Client,
			TurnstileSiteKey:  cfg.TurnstileSiteKey,
			NetworkDailyLimit: benfitNetworkDailyLimit,
			ClaimMinuteLimit:  benfitClaimMinuteLimit,
		}
		logger.Info("benefit: runtime initialized",
			"turnstile", turnstileVerifier != nil,
			"opus8", opus8Client != nil,
		)
	}

	// ── 11. HTTP server ───────────────────────────────────────────────────────
	srv := httpapi.New(
		cfg, repo, sessions, limiter, stor, localStore,
		viewBuf, searchCache, paidAccessClient, benefitRuntime, logger,
	)

	httpSrv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           srv.Handler(),
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}

	// Serve in background
	go func() {
		ln, err := net.Listen("tcp", httpSrv.Addr)
		if err != nil {
			logger.Error("listen failed", "addr", httpSrv.Addr, "error", err)
			cancel()
			return
		}
		logger.Info("server listening", "addr", httpSrv.Addr, "version", version)
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "error", err)
			cancel()
		}
	}()

	// ── 11. Wait for shutdown signal ──────────────────────────────────────────
	<-ctx.Done()
	logger.Info("shutdown signal received, draining...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()

	// Flush view buffer before closing
	viewBuf.Flush(shutdownCtx)

	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		return err
	}
	logger.Info("server stopped cleanly")
	return nil
}
