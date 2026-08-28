package config

import (
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/auth"
	"github.com/Doni-15/SwaDrive/server/internal/files"
	"github.com/Doni-15/SwaDrive/server/internal/uploads"
)

func TestLoadServerRequiresPathsAndParsesOverrides(t *testing.T) {
	if _, err := LoadServer(func(string) string { return "" }); err == nil {
		t.Fatal("LoadServer() without paths succeeded; want error")
	}

	values := map[string]string{
		"SWADRIVE_DATABASE_PATH":            "/tmp/state.db",
		"SWADRIVE_STORAGE_ROOT":             "/tmp/storage",
		"SWADRIVE_STORAGE_VOLUME_ID":        "test-volume-123",
		"SWADRIVE_LISTEN_ADDRESS":           "127.0.0.1:9090",
		"SWADRIVE_STORAGE_RESERVE_BYTES":    "4096",
		"SWADRIVE_UPLOAD_CLEANUP_INTERVAL":  "30m",
		"SWADRIVE_MAX_CONCURRENT_ARGON2":    "3",
		"SWADRIVE_MAX_CONCURRENT_CHUNKS":    "6",
		"SWADRIVE_MAX_CONCURRENT_DOWNLOADS": "12",
	}
	configuration, err := LoadServer(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if configuration.DatabasePath != "/tmp/state.db" ||
		configuration.StorageRoot != "/tmp/storage" ||
		configuration.StorageVolumeID != "test-volume-123" {
		t.Fatalf(
			"storage configuration = %q, %q, %q; want configured values",
			configuration.DatabasePath,
			configuration.StorageRoot,
			configuration.StorageVolumeID,
		)
	}
	if configuration.ListenAddress != "127.0.0.1:9090" || configuration.StorageReserveBytes != 4096 || configuration.UploadCleanupInterval != 30*time.Minute || configuration.MaxConcurrentArgon2 != 3 || configuration.MaxConcurrentChunks != 6 || configuration.MaxConcurrentDownloads != 12 {
		t.Fatalf("configuration = %+v; want parsed overrides", configuration)
	}

	defaults := map[string]string{
		"SWADRIVE_DATABASE_PATH":     "/tmp/state.db",
		"SWADRIVE_STORAGE_ROOT":      "/tmp/storage",
		"SWADRIVE_STORAGE_VOLUME_ID": "test-volume-123",
	}
	configuration, err = LoadServer(func(key string) string { return defaults[key] })
	if err != nil {
		t.Fatalf("LoadServer(defaults) error = %v", err)
	}
	if configuration.ListenAddress != defaultListenAddress || configuration.StorageReserveBytes != uploads.DefaultReserveBytes || configuration.MaxConcurrentArgon2 != auth.DefaultArgon2Limit || configuration.MaxConcurrentChunks != uploads.DefaultConcurrentChunks || configuration.MaxConcurrentDownloads != files.DefaultConcurrentDownloads {
		t.Fatalf("defaults = %+v; want listen and reserve defaults", configuration)
	}
	if configuration.ListenAddress != "127.0.0.1:8080" {
		t.Fatalf("default listen address = %q; want loopback-only", configuration.ListenAddress)
	}
}

func TestLoadServerRequiresStorageVolumeIdentity(t *testing.T) {
	values := map[string]string{
		"SWADRIVE_DATABASE_PATH": "/tmp/state.db",
		"SWADRIVE_STORAGE_ROOT":  "/tmp/storage",
	}

	if _, err := LoadServer(func(key string) string { return values[key] }); err == nil {
		t.Fatal("LoadServer() without storage volume ID succeeded; want error")
	}
}

func TestLoadServerRejectsUnsafeConcurrencyLimits(t *testing.T) {
	for _, key := range []string{
		"SWADRIVE_MAX_CONCURRENT_ARGON2",
		"SWADRIVE_MAX_CONCURRENT_CHUNKS",
		"SWADRIVE_MAX_CONCURRENT_DOWNLOADS",
	} {
		values := map[string]string{
			"SWADRIVE_DATABASE_PATH":     "/tmp/state.db",
			"SWADRIVE_STORAGE_ROOT":      "/tmp/storage",
			"SWADRIVE_STORAGE_VOLUME_ID": "test-volume-123",
			key:                          "0",
		}
		if _, err := LoadServer(func(name string) string { return values[name] }); err == nil {
			t.Fatalf("LoadServer(%s=0) succeeded; want error", key)
		}
	}
}
