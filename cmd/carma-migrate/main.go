package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/config"
	"github.com/bitofbytes-io/carma/internal/database"
	"github.com/bitofbytes-io/carma/migrations"
	"github.com/jackc/pgx/v5"
)

func main() {
	logger := newLogger(os.Stderr)
	slog.SetDefault(logger)
	if err := run(context.Background(), logger); err != nil {
		logger.Error("carma migrate stopped", "error", err)
		os.Exit(1)
	}
}

func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, nil))
}

func run(ctx context.Context, logger *slog.Logger) error {
	databaseURL, err := config.LoadDatabaseURL()
	if err != nil {
		return err
	}
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	location, err := config.LoadLocation()
	if err != nil {
		return err
	}
	version, err := latestMigrationVersion(migrations.FS)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	logger.Info("migration starting", "version", version)
	if err := database.Migrate(ctx, conn, migrations.FS, location); err != nil {
		return err
	}
	applied, err := schemaVersion(ctx, conn)
	if err != nil {
		return err
	}
	logger.Info("migrations applied", "version", applied)
	return nil
}

func latestMigrationVersion(files fs.FS) (string, error) {
	names, err := fs.Glob(files, "*.up.sql")
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", errors.New("no migrations found")
	}
	sort.Strings(names)
	return strings.TrimSuffix(names[len(names)-1], ".up.sql"), nil
}

// schemaVersion returns the newest applied migration. Versions are zero-padded,
// so lexicographic order matches apply order.
func schemaVersion(ctx context.Context, conn *pgx.Conn) (string, error) {
	var version string
	err := conn.QueryRow(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("schema version missing after migration")
	}
	if err != nil {
		return "", fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}
