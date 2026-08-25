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
)

type PasswordReader func(prompt string) (string, error)

func Run(ctx context.Context, arguments []string, stdout, stderr io.Writer, readPassword PasswordReader) error {
	if len(arguments) == 0 {
		return errors.New("usage: swadrive-admin <bootstrap-owner|reindex> [options]")
	}
	switch arguments[0] {
	case "bootstrap-owner":
		return bootstrapOwner(ctx, arguments[1:], stdout, stderr, readPassword)
	case "reindex":
		return reindex(ctx, arguments[1:], stdout, stderr)
	default:
		return errors.New("usage: swadrive-admin <bootstrap-owner|reindex> [options]")
	}
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
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*databasePath) == "" || strings.TrimSpace(*storagePath) == "" {
		return errors.New("reindex requires -database PATH and -storage PATH")
	}

	db, err := database.Open(ctx, *databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		return err
	}
	storageManager, err := storage.Open(*storagePath)
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
