// Package config loads explicit local server configuration.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/auth"
	"github.com/Doni-15/SwaDrive/server/internal/files"
	"github.com/Doni-15/SwaDrive/server/internal/uploads"
)

const (
	defaultListenAddress   = ":8080"
	defaultCleanupInterval = 15 * time.Minute
)

type Server struct {
	DatabasePath           string
	StorageRoot            string
	StorageVolumeID        string
	ListenAddress          string
	StorageReserveBytes    uint64
	UploadCleanupInterval  time.Duration
	MaxConcurrentArgon2    int
	MaxConcurrentChunks    int
	MaxConcurrentDownloads int
}

func LoadServer(getenv func(string) string) (Server, error) {
	configuration := Server{
		DatabasePath:           strings.TrimSpace(getenv("SWADRIVE_DATABASE_PATH")),
		StorageRoot:            strings.TrimSpace(getenv("SWADRIVE_STORAGE_ROOT")),
		StorageVolumeID:        strings.TrimSpace(getenv("SWADRIVE_STORAGE_VOLUME_ID")),
		ListenAddress:          strings.TrimSpace(getenv("SWADRIVE_LISTEN_ADDRESS")),
		StorageReserveBytes:    uploads.DefaultReserveBytes,
		UploadCleanupInterval:  defaultCleanupInterval,
		MaxConcurrentArgon2:    auth.DefaultArgon2Limit,
		MaxConcurrentChunks:    uploads.DefaultConcurrentChunks,
		MaxConcurrentDownloads: files.DefaultConcurrentDownloads,
	}
	if configuration.DatabasePath == "" ||
		configuration.StorageRoot == "" ||
		configuration.StorageVolumeID == "" {
		return Server{}, errors.New(
			"SWADRIVE_DATABASE_PATH, SWADRIVE_STORAGE_ROOT, and SWADRIVE_STORAGE_VOLUME_ID are required",
		)
	}
	if configuration.ListenAddress == "" {
		configuration.ListenAddress = defaultListenAddress
	}

	if value := strings.TrimSpace(getenv("SWADRIVE_STORAGE_RESERVE_BYTES")); value != "" {
		reserve, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return Server{}, fmt.Errorf("parse SWADRIVE_STORAGE_RESERVE_BYTES: %w", err)
		}
		configuration.StorageReserveBytes = reserve
	}
	if value := strings.TrimSpace(getenv("SWADRIVE_UPLOAD_CLEANUP_INTERVAL")); value != "" {
		interval, err := time.ParseDuration(value)
		if err != nil || interval <= 0 {
			return Server{}, errors.New("SWADRIVE_UPLOAD_CLEANUP_INTERVAL must be a positive duration")
		}
		configuration.UploadCleanupInterval = interval
	}
	if value := strings.TrimSpace(getenv("SWADRIVE_MAX_CONCURRENT_ARGON2")); value != "" {
		limit, err := parseConcurrency(value, auth.MaximumArgon2Limit)
		if err != nil {
			return Server{}, fmt.Errorf("parse SWADRIVE_MAX_CONCURRENT_ARGON2: %w", err)
		}
		configuration.MaxConcurrentArgon2 = limit
	}
	if value := strings.TrimSpace(getenv("SWADRIVE_MAX_CONCURRENT_CHUNKS")); value != "" {
		limit, err := parseConcurrency(value, uploads.MaximumConcurrentChunks)
		if err != nil {
			return Server{}, fmt.Errorf("parse SWADRIVE_MAX_CONCURRENT_CHUNKS: %w", err)
		}
		configuration.MaxConcurrentChunks = limit
	}
	if value := strings.TrimSpace(getenv("SWADRIVE_MAX_CONCURRENT_DOWNLOADS")); value != "" {
		limit, err := parseConcurrency(value, files.MaximumConcurrentDownloads)
		if err != nil {
			return Server{}, fmt.Errorf("parse SWADRIVE_MAX_CONCURRENT_DOWNLOADS: %w", err)
		}
		configuration.MaxConcurrentDownloads = limit
	}
	return configuration, nil
}

func parseConcurrency(value string, maximum int) (int, error) {
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > maximum {
		return 0, fmt.Errorf("must be an integer from 1 through %d", maximum)
	}
	return limit, nil
}
