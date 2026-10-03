// Package testdb gives tests an isolated, migrated PostgreSQL schema.
package testdb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/database"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EnvVar names the PostgreSQL URL that database-backed tests run against.
const EnvVar = "CARMA_TEST_DATABASE_URL"

// URL creates a schema, applies every migration to it, and returns a database
// URL whose search_path selects it. The schema is dropped when the test ends.
//
// Without CARMA_TEST_DATABASE_URL the test is skipped locally, but fails when
// CI is set, so CI can never pass without running the database-backed tests.
func URL(t testing.TB) string {
	t.Helper()
	baseURL := os.Getenv(EnvVar)
	if baseURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s must be set in CI", EnvVar)
		}
		t.Skipf("set %s (for example via `make test-integration`) to run PostgreSQL-backed tests", EnvVar)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Fatalf("connect to %s: %v", EnvVar, err)
	}
	defer func() { _ = admin.Close(context.Background()) }()
	schema := "carma_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		connection, err := pgx.Connect(cleanupCtx, baseURL)
		if err != nil {
			t.Errorf("drop test schema %s: %v", schema, err)
			return
		}
		defer func() { _ = connection.Close(context.Background()) }()
		if _, err = connection.Exec(cleanupCtx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop test schema %s: %v", schema, err)
		}
	})
	scopedURL, err := WithParameter(baseURL, "search_path", schema)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := pgx.Connect(ctx, scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close(context.Background()) }()
	if err = database.Migrate(ctx, connection, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return scopedURL
}

// Store returns a store on a fresh schema from URL. It is closed when the test
// ends.
func Store(t testing.TB) *repository.Postgres {
	t.Helper()
	store, err := repository.NewPostgres(context.Background(), URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

// WithParameter sets a connection parameter in a PostgreSQL URL.
func WithParameter(databaseURL, key, value string) (string, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parse test database URL: %w", err)
	}
	query := parsed.Query()
	query.Set(key, value)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
