package storage

import (
	"path/filepath"
	"testing"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/config"
)

func TestManagedStorageSeparation(t *testing.T) {
	root := t.TempDir()
	public := filepath.Join(root, "public")
	for _, private := range []string{public, filepath.Join(public, "private"), root} {
		if _, _, err := NewManagedAdapters(&config.Config{StorageDriver: "local", LocalStorageRoot: public, ManagedLocalRoot: private}); err == nil {
			t.Fatalf("overlap allowed: %s", private)
		}
	}
	active, stores, err := NewManagedAdapters(&config.Config{StorageDriver: "local", LocalStorageRoot: public})
	if err != nil || active == nil || stores["local"] != active {
		t.Fatalf("separate default root: %v", err)
	}
	for _, driver := range []string{"r2", "oss"} {
		if _, _, err := NewManagedAdapters(&config.Config{StorageDriver: driver}); err == nil {
			t.Fatal("missing private bucket accepted")
		}
	}
	if _, _, err := NewManagedAdapters(&config.Config{StorageDriver: "r2", ManagedMediaPrivateOriginConfirmed: true, R2Bucket: "same", ManagedR2Bucket: "same"}); err == nil {
		t.Fatal("public bucket reused")
	}
	cfg := &config.Config{StorageDriver: "oss", ManagedMediaPrivateOriginConfirmed: true, AliyunOSSBucket: "public", ManagedOSSBucket: "private", AliyunOSSRegion: "cn-hangzhou", AliyunOSSAccessKeyID: "id", AliyunOSSAccessKeySecret: "secret", AliyunOSSEndpoint: "https://public.oss-cn-hangzhou.aliyuncs.com"}
	active, _, err = NewManagedAdapters(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if active.(*OSSAdapter).endpoint != "https://private.oss-cn-hangzhou.aliyuncs.com" {
		t.Fatal("inherited public bucket endpoint")
	}
}
