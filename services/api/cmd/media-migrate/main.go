// media-migrate audits by default. No production mutation is implicit in a
// release or startup; all mutating stages require explicit command-line flags.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/config"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/mediamigrate"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "media migration:", err)
		os.Exit(1)
	}
}
func run() error {
	apply := flag.Bool("apply", false, "copy verified objects to private storage and rewrite DB references; retain public originals")
	retire := flag.Bool("retire-public-originals", false, "DESTRUCTIVE: remove only previously copied originals; requires approved backup/rollback")
	purge := flag.Bool("confirm-cache-purge", false, "record operator confirmation that old CDN URLs were purged/disabled after retirement")
	maintenance := flag.Bool("maintenance-confirmed", false, "confirm article/comment/product/tool writes are stopped for copy or retirement")
	flag.Parse()
	modes := 0
	for _, v := range []bool{*apply, *retire, *purge} {
		if v {
			modes++
		}
	}
	if modes > 1 {
		return fmt.Errorf("run each stage separately")
	}
	if (*apply || *retire) && !*maintenance {
		return fmt.Errorf("copy and retirement require --maintenance-confirmed; stop content writes before running")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if modes > 0 {
		sources, err := sourceAdapters(cfg)
		if err != nil {
			return err
		}
		if *apply {
			target, _, err := storage.NewManagedAdapters(cfg)
			if err != nil {
				return err
			}
			if err := mediamigrate.Copy(ctx, db, sources, target); err != nil {
				return err
			}
		}
		if *retire {
			_, private, err := storage.NewManagedAdapters(cfg)
			if err != nil {
				return err
			}
			if err := mediamigrate.Retire(ctx, db, sources, private); err != nil {
				return err
			}
		}
		if *purge {
			if err := mediamigrate.ConfirmPurge(ctx, db, sources); err != nil {
				return err
			}
		}
	}
	plan, err := mediamigrate.Audit(ctx, db)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(plan)
}

func sourceAdapters(cfg *config.Config) (map[string]storage.ManagedAdapter, error) {
	stores := map[string]storage.ManagedAdapter{}
	if cfg.StorageDriver == "local" || cfg.ManagedLocalRoot != "" {
		a, err := storage.NewLocalAdapter(cfg.LocalStorageRoot, cfg.PublicUploadBaseURL)
		if err != nil {
			return nil, err
		}
		stores["local"] = a
	}
	if cfg.R2Bucket != "" {
		a, err := storage.NewR2Adapter(cfg.R2AccountID, cfg.R2Bucket, cfg.R2AccessKeyID, cfg.R2SecretAccessKey, cfg.R2Endpoint, cfg.R2PublicBaseURL, cfg.R2Prefix)
		if err != nil {
			return nil, err
		}
		stores["r2"] = a
	}
	if cfg.AliyunOSSBucket != "" {
		a, err := storage.NewOSSAdapter(cfg.AliyunOSSRegion, cfg.AliyunOSSBucket, cfg.AliyunOSSAccessKeyID, cfg.AliyunOSSAccessKeySecret, cfg.AliyunOSSEndpoint, cfg.AliyunOSSPublicBaseURL, cfg.AliyunOSSPrefix)
		if err != nil {
			return nil, err
		}
		stores["oss"] = a
	}
	return stores, nil
}
