package main

import (
	"context"
	"errors"
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
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(parentContext context.Context, configuration config.Server, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(parentContext)
	defer cancel()

	db, err := database.Open(ctx, configuration.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		return err
	}

	storageManager, err := storage.Open(configuration.StorageRoot)
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
	cleanupErrors := make(chan error, 1)
	go func() {
		cleanupErrors <- uploadService.RunCleanup(ctx, configuration.UploadCleanupInterval)
	}()

	var runError error
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			runError = err
		}
	case err := <-cleanupErrors:
		if err != nil {
			runError = err
		}
	}

	cancel()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	shutdownError := server.Shutdown(shutdownContext)
	return errors.Join(runError, shutdownError)
}
