package database

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const migrationAdvisoryLockID int64 = 0x4341524d41 // "CARMA"

func Migrate(ctx context.Context, conn *pgx.Conn, files fs.FS) (result error) {
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationAdvisoryLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// Use a fresh bounded context so cancellation of the migration request does
		// not leave this session holding the lock until the connection is closed.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		unlockErr := conn.QueryRow(unlockCtx, `SELECT pg_advisory_unlock($1)`, migrationAdvisoryLockID).Scan(&unlocked)
		if unlockErr == nil && !unlocked {
			unlockErr = errors.New("migration advisory lock was not held")
		}
		if unlockErr != nil {
			result = errors.Join(result, fmt.Errorf("release migration lock: %w", unlockErr))
		}
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(files, "*.up.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		version := strings.TrimSuffix(name, ".up.sql")
		var applied bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(body)); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, version)
		}
		if err != nil {
			applyErr := fmt.Errorf("apply %s: %w", name, err)
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
				return errors.Join(applyErr, fmt.Errorf("rollback %s: %w", name, rollbackErr))
			}
			return applyErr
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
