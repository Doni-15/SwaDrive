// Package admincli implements administrator-controlled local commands.
package admincli

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/auth"
	"github.com/Doni-15/SwaDrive/server/internal/database"
	"github.com/Doni-15/SwaDrive/server/internal/files"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
	"github.com/Doni-15/SwaDrive/server/internal/uploads"
)

type PasswordReader func(prompt string) (string, error)

func Run(ctx context.Context, arguments []string, stdout, stderr io.Writer, readPassword PasswordReader) error {
	if len(arguments) == 0 {
		return errors.New("usage: swadrive-admin <bootstrap-owner|reindex|reconcile-upload-parts> [options]")
	}
	switch arguments[0] {
	case "bootstrap-owner":
		return bootstrapOwner(ctx, arguments[1:], stdout, stderr, readPassword)
	case "reindex":
		return reindex(ctx, arguments[1:], stdout, stderr)
	case "reconcile-upload-parts":
		return reconcileUploadParts(ctx, arguments[1:], stdout, stderr)
	default:
		return errors.New("usage: swadrive-admin <bootstrap-owner|reindex|reconcile-upload-parts> [options]")
	}
}

func reconcileUploadParts(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("reconcile-upload-parts", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String("database", "", "SQLite database path")
	storagePath := flags.String("storage", "", "content storage root")
	volumeID := flags.String("volume-id", "", "expected storage volume ID")
	minimumAge := flags.Duration("minimum-age", uploads.DefaultOrphanPartMinimumAge, "minimum orphan age")
	scanLimit := flags.Int("scan-limit", uploads.DefaultOrphanPartScanLimit, "maximum candidate part files")
	apply := flags.Bool("apply", false, "remove confirmed old orphan parts")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*databasePath) == "" || strings.TrimSpace(*storagePath) == "" || strings.TrimSpace(*volumeID) == "" {
		return errors.New("reconcile-upload-parts requires -database PATH, -storage PATH, and -volume-id ID")
	}
	if *minimumAge <= 0 || *scanLimit < 1 || *scanLimit > uploads.MaximumOrphanPartScanLimit {
		return errors.New("reconcile-upload-parts has invalid -minimum-age or -scan-limit")
	}

	processLock, err := database.AcquireProcessLock(*databasePath)
	if err != nil {
		return err
	}
	defer processLock.Close()
	db, err := database.Open(ctx, *databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		return err
	}
	storageManager, err := storage.OpenVerified(*storagePath, *volumeID)
	if err != nil {
		return err
	}
	defer storageManager.Close()

	reconciler := uploads.NewOrphanPartReconciler(uploads.NewSQLiteRepository(db), storageManager, nil)
	result, err := reconciler.Reconcile(ctx, *minimumAge, *scanLimit, *apply)
	if err != nil {
		return err
	}
	mode := "dry-run"
	if *apply {
		mode = "applied"
	}
	_, err = fmt.Fprintf(stdout, "Upload part reconciliation %s: scanned=%d orphans=%d removed=%d.\n", mode, result.Scanned, result.Orphans, result.Removed)
	return err
}

func bootstrapOwner(ctx context.Context, arguments []string, stdout, stderr io.Writer, readPassword PasswordReader) error {
	if readPassword == nil {
		return errors.New("password reader is required")
	}

	flags := flag.NewFlagSet("bootstrap-owner", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String("database", "", "SQLite database path")
	username := flags.String("username", "", "initial owner username")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*databasePath) == "" || strings.TrimSpace(*username) == "" {
		return errors.New("bootstrap-owner requires -database PATH and -username USERNAME")
	}

	processLock, err := database.AcquireProcessLock(*databasePath)
	if err != nil {
		return err
	}
	defer processLock.Close()

	password, err := readPassword("Password: ")
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	confirmation, err := readPassword("Confirm password: ")
	if err != nil {
		return fmt.Errorf("read password confirmation: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(password), []byte(confirmation)) != 1 {
		return errors.New("password confirmation does not match")
	}

	db, err := database.Open(ctx, *databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		return err
	}

	auditService := audit.NewService(audit.NewSQLiteRepository(db), nil)
	passwordManager, err := auth.NewPasswordManager(auth.DefaultArgon2Limit)
	if err != nil {
		return err
	}
	authService, err := auth.NewService(
		auth.NewSQLiteRepository(db),
		auditService,
		auth.NewLoginLimiter(auth.DefaultLimiterEntries),
		passwordManager,
		nil,
	)
	if err != nil {
		return err
	}
	owner, err := authService.BootstrapOwner(ctx, *username, password, "")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Owner %q created.\n", owner.Username)
	return err
}

func reindex(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("reindex", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String("database", "", "SQLite database path")
	storagePath := flags.String("storage", "", "content storage root")
	volumeID := flags.String("volume-id", "", "expected storage volume ID")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 ||
		strings.TrimSpace(*databasePath) == "" ||
		strings.TrimSpace(*storagePath) == "" ||
		strings.TrimSpace(*volumeID) == "" {
		return errors.New("reindex requires -database PATH, -storage PATH, and -volume-id ID")
	}

	processLock, err := database.AcquireProcessLock(*databasePath)
	if err != nil {
		return err
	}
	defer processLock.Close()

	db, err := database.Open(ctx, *databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		return err
	}
	storageManager, err := storage.OpenVerified(*storagePath, *volumeID)
	if err != nil {
		return err
	}
	defer storageManager.Close()

	rebuilder := files.NewRebuilder(files.NewSQLiteFileIndexRepository(db), storageManager, nil)
	result, err := rebuilder.Rebuild(ctx, files.WriteProgress(stdout))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Reindex complete: generation=%d entries=%d obsolete_rows_removed=%d.\n", result.GenerationID, result.Indexed, result.ObsoleteRows)
	return err
}
