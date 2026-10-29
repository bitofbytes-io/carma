package database_test

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/database"
	"github.com/bitofbytes-io/carma/internal/testdb"
	"github.com/bitofbytes-io/carma/migrations"
	"github.com/jackc/pgx/v5"
)

// TestMigrationsRoundTripUpDown migrates a fresh schema, rolls every migration
// back to zero, and migrates up again. A down that errors, leaves its version
// recorded, or leaves objects that the next up cannot recreate fails the test.
func TestMigrationsRoundTripUpDown(t *testing.T) {
	databaseURL := testdb.URL(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })

	versions := migrationVersions(t)
	wantTables := []string{
		"attachments",
		"odometer_readings",
		"recall_dismissals",
		"recall_notifications",
		"records",
		"reminder_digests",
		"reminder_notifications",
		"reminders",
		"schema_migrations",
		"service_types",
		"user_sessions",
		"users",
		"vehicle_recall_checks",
		"vehicle_recalls",
		"vehicles",
	}
	assertTables(t, ctx, connection, wantTables)
	assertVersions(t, ctx, connection, versions)

	// Reapply each later migration immediately after its own down. That catches
	// a down which clears its version but leaves objects behind: the following
	// up fails while those objects still exist. The initial down drops every
	// application table, so it is checked by rolling all the way to zero.
	for i := len(versions) - 1; i >= 1; i-- {
		applyDown(t, ctx, connection, versions[i])
		if err := database.Migrate(ctx, connection, migrations.FS, time.UTC); err != nil {
			t.Fatalf("reapply %s: %v", versions[i], err)
		}
		assertTables(t, ctx, connection, wantTables)
		assertVersions(t, ctx, connection, versions)
	}

	for i := len(versions) - 1; i >= 0; i-- {
		applyDown(t, ctx, connection, versions[i])
	}
	assertTables(t, ctx, connection, []string{"schema_migrations"})
	assertVersions(t, ctx, connection, []string{})

	if err := database.Migrate(ctx, connection, migrations.FS, time.UTC); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	assertTables(t, ctx, connection, wantTables)
	assertVersions(t, ctx, connection, versions)
}

// migrationVersions returns embedded migration versions in apply order. Every
// up file must have a matching down file.
func migrationVersions(t *testing.T) []string {
	t.Helper()
	up := versionsWithSuffix(t, ".up.sql")
	down := versionsWithSuffix(t, ".down.sql")
	if len(up) == 0 {
		t.Fatal("no migrations embedded")
	}
	if !slices.Equal(up, down) {
		t.Fatalf("up versions %v do not match down versions %v", up, down)
	}
	return up
}

func versionsWithSuffix(t *testing.T, suffix string) []string {
	t.Helper()
	names, err := fs.Glob(migrations.FS, "*"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	versions := make([]string, 0, len(names))
	for _, name := range names {
		version, ok := strings.CutSuffix(name, suffix)
		if !ok {
			t.Fatalf("migration %s does not end in %s", name, suffix)
		}
		versions = append(versions, version)
	}
	return versions
}

func applyDown(t *testing.T, ctx context.Context, connection *pgx.Conn, version string) {
	t.Helper()
	name := version + ".down.sql"
	body, err := fs.ReadFile(migrations.FS, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, string(body)); err != nil {
		t.Fatalf("down %s: %v", name, err)
	}
	var applied bool
	if err = connection.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatalf("down %s left %s recorded in schema_migrations", name, version)
	}
}

func assertTables(t *testing.T, ctx context.Context, connection *pgx.Conn, want []string) {
	t.Helper()
	rows, err := connection.Query(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
		ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("tables = %v, want %v", got, want)
	}
}

func assertVersions(t *testing.T, ctx context.Context, connection *pgx.Conn, want []string) {
	t.Helper()
	rows, err := connection.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("schema_migrations = %v, want %v", got, want)
	}
}
