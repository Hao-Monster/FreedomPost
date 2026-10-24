package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/config"
)

// NewManagedAdapters keeps private media physically separate from public
// uploads. A missing historical driver is never replaced by a public URL.
func NewManagedAdapters(cfg *config.Config) (ManagedAdapter, map[string]ManagedAdapter, error) {
	stores := map[string]ManagedAdapter{}
	root := cfg.ManagedLocalRoot
	if root == "" {
		root = cfg.LocalStorageRoot + "-private"
	}
	if cfg.StorageDriver == "local" || cfg.ManagedLocalRoot != "" {
		public, err := filepath.Abs(cfg.LocalStorageRoot)
		if err != nil {
			return nil, nil, err
		}
		private, err := filepath.Abs(root)
		if err != nil {
			return nil, nil, err
		}
		for _, path := range []string{public, private} {
			if err := os.MkdirAll(path, 0700); err != nil {
				return nil, nil, err
			}
		}
		public, err = filepath.EvalSymlinks(public)
		if err != nil {
			return nil, nil, err
		}
		private, err = filepath.EvalSymlinks(private)
		if err != nil {
			return nil, nil, err
		}
		if pathsOverlap(public, private) {
			return nil, nil, fmt.Errorf("managed local root must be separate from public storage")
		}
		adapter, err := NewLocalAdapter(private, "")
		if err != nil {
			return nil, nil, err
		}
		stores["local"] = adapter
	}
	if cfg.StorageDriver == "r2" || cfg.ManagedR2Bucket != "" {
		if !cfg.ManagedMediaPrivateOriginConfirmed || cfg.ManagedR2Bucket == "" || cfg.ManagedR2Bucket == cfg.R2Bucket {
			return nil, nil, fmt.Errorf("managed R2 requires a separate private bucket and MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED=true")
		}
		adapter, err := NewR2Adapter(cfg.R2AccountID, cfg.ManagedR2Bucket, cfg.R2AccessKeyID, cfg.R2SecretAccessKey, cfg.R2Endpoint, "", cfg.ManagedR2Prefix)
		if err != nil {
			return nil, nil, err
		}
		stores["r2"] = adapter
	}
	if cfg.StorageDriver == "oss" || cfg.ManagedOSSBucket != "" {
		if !cfg.ManagedMediaPrivateOriginConfirmed || cfg.ManagedOSSBucket == "" || cfg.ManagedOSSBucket == cfg.AliyunOSSBucket {
			return nil, nil, fmt.Errorf("managed OSS requires a separate private bucket and MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED=true")
		}
		endpoint := cfg.ManagedOSSEndpoint
		adapter, err := NewOSSAdapter(cfg.AliyunOSSRegion, cfg.ManagedOSSBucket, cfg.AliyunOSSAccessKeyID, cfg.AliyunOSSAccessKeySecret, endpoint, "", cfg.ManagedOSSPrefix)
		if err != nil {
			return nil, nil, err
		}
		stores["oss"] = adapter
	}
	active := stores[cfg.StorageDriver]
	if active == nil {
		return nil, nil, fmt.Errorf("managed storage driver is unavailable")
	}
	return active, stores, nil
}

func pathsOverlap(a, b string) bool {
	within := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	return within(a, b) || within(b, a)
}
