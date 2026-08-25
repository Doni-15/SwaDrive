package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/auth"
	"github.com/Doni-15/SwaDrive/server/internal/config"
	"github.com/Doni-15/SwaDrive/server/internal/database"
	"github.com/Doni-15/SwaDrive/server/internal/files"
	"github.com/Doni-15/SwaDrive/server/internal/httpapi"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
	"github.com/Doni-15/SwaDrive/server/internal/uploads"
)

const maximumHTTPHeaderBytes = 64 << 10

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	configuration, err := config.LoadServer(os.Getenv)
	if err == nil {
		err = run(ctx, configuration, logger)
	}
	if err != nil {
		logStartupFailure(logger, err)
		os.Exit(1)
	}
}

func logStartupFailure(logger *slog.Logger, err error) {
	category := "startup_failure"
	switch {
	case errors.Is(err, database.ErrProcessLockBusy):
		category = "database_in_use"
	case errors.Is(err, storage.ErrStorageVolumeRequired), errors.Is(err, storage.ErrStorageVolumeMismatch), errors.Is(err, storage.ErrDifferentFilesystem):
		category = "storage_validation"
	case errors.Is(err, files.ErrIndexInconsistent):
		category = "metadata_inconsistent"
	}
	// Startup errors can wrap administrator-controlled physical paths. Keep the
	// operational log useful at a high level without exporting those paths.
	logger.Error("server stopped", "error_type", category)
}

func run(parentContext context.Context, configuration config.Server, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(parentContext)
	defer cancel()

	processLock, err := database.AcquireProcessLock(configuration.DatabasePath)
	if err != nil {
		return err
	}
	defer processLock.Close()

	db, err := database.Open(ctx, configuration.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		return err
	}

	storageManager, err := storage.OpenVerified(
		configuration.StorageRoot,
		configuration.StorageVolumeID,
	)
	if err != nil {
		return err
	}
	defer storageManager.Close()

	auditService := audit.NewService(audit.NewSQLiteRepository(db), nil)
	mutationCoordinator := storage.NewMutationCoordinator()
	passwordManager, err := auth.NewPasswordManager(configuration.MaxConcurrentArgon2)
	if err != nil {
		return err
	}
	authService, err := auth.NewService(auth.NewSQLiteRepository(db), auditService, auth.NewLoginLimiter(auth.DefaultLimiterEntries), passwordManager, nil)
	if err != nil {
		return err
	}
	fileIndexRepository := files.NewSQLiteFileIndexRepository(db)
	filesService := files.NewService(storageManager, files.NewSQLiteTrashRepository(db), fileIndexRepository, mutationCoordinator, auditService, configuration.MaxConcurrentDownloads, nil)
	if _, err := filesService.ReconcileTrash(ctx); err != nil {
		return err
	}
	uploadService := uploads.NewService(uploads.NewSQLiteRepository(db), storageManager, mutationCoordinator, auditService, configuration.StorageReserveBytes, configuration.MaxConcurrentChunks, nil)
	if _, err := uploadService.ReconcileFinalizing(ctx); err != nil {
		return err
	}
	if err := fileIndexRepository.CheckHealthy(ctx); err != nil {
		return errors.Join(files.ErrIndexInconsistent, err)
	}

	handler := httpapi.NewHandler(httpapi.Dependencies{
		Auth:    authService,
		Audit:   auditService,
		Files:   filesService,
		Uploads: uploadService,
		Logger:  logger,
	})
	server := &http.Server{
		Addr:              configuration.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    maximumHTTPHeaderBytes,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("SwaDrive server listening", "address", server.Addr)
		serverErrors <- server.ListenAndServe()
	}()
	cleanupDone := make(chan struct{})
	var cleanupError error
	go func() {
		defer close(cleanupDone)
		cleanupError = uploadService.RunCleanup(ctx, configuration.UploadCleanupInterval)
	}()

	var runError error
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			runError = err
		}
	case <-cleanupDone:
		if cleanupError != nil {
			runError = cleanupError
		}
	}

	cancel()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	shutdownError := server.Shutdown(shutdownContext)
	// Give normal cleanup a bounded opportunity to observe cancellation before
	// dependent resources close. An overrun fails shutdown; main then exits the
	// process. This is deliberately not an unbounded goroutine-join guarantee.
	select {
	case <-cleanupDone:
	case <-shutdownContext.Done():
		return errors.Join(runError, shutdownError, fmt.Errorf("wait for upload cleanup worker: %w", shutdownContext.Err()))
	}
	return errors.Join(runError, shutdownError, cleanupError)
}
