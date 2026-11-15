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

// TestVehicleTrimMigrationPreservesData applies 012 over rows saved before it,
// checks the new columns' defaults and that the rows survive, then rolls 012
// back and checks the columns are gone and the rows remain.
func TestVehicleTrimMigrationPreservesData(t *testing.T) {
	const version = "012_vehicle_trim"
	databaseURL := testdb.URL(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })

	applyDown(t, ctx, connection, version)
	assertColumns(t, ctx, connection, "vehicles", nil, []string{"trim"})
	assertColumns(t, ctx, connection, "vehicle_recall_checks", nil, []string{"trim", "vin", "match_version"})

	if _, err = connection.Exec(ctx, `
		INSERT INTO vehicles(id,nickname,year,make,model,vin) VALUES('00000000-0000-0000-0000-000000000001','Benz',2024,'Mercedes-Benz','E-Class','W1KLF6BB7VA343747');
		INSERT INTO vehicle_recall_checks(vehicle_id,checked_at,model_year,make,model) VALUES('00000000-0000-0000-0000-000000000001','2026-11-01T00:00:00Z',2024,'Mercedes-Benz','E-Class')`); err != nil {
		t.Fatal(err)
	}

	if err = database.Migrate(ctx, connection, migrations.FS, time.UTC); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	assertVersions(t, ctx, connection, migrationVersions(t))
	assertColumns(t, ctx, connection, "vehicles", []string{"trim"}, nil)
	assertColumns(t, ctx, connection, "vehicle_recall_checks", []string{"trim", "vin", "match_version"}, nil)
	assertPreserved := func() {
		t.Helper()
		var nickname, vin string
		var checkedYear int
		if err := connection.QueryRow(ctx, `SELECT v.nickname,v.vin,c.model_year FROM vehicles v JOIN vehicle_recall_checks c ON c.vehicle_id=v.id`).Scan(&nickname, &vin, &checkedYear); err != nil {
			t.Fatalf("existing rows lost: %v", err)
		}
		if nickname != "Benz" || vin != "W1KLF6BB7VA343747" || checkedYear != 2024 {
			t.Fatalf("existing rows changed: %q %q %d", nickname, vin, checkedYear)
		}
	}
	assertPreserved()
	var trim, checkTrim, checkVIN string
	var matchVersion int
	if err = connection.QueryRow(ctx, `SELECT v.trim,c.trim,c.vin,c.match_version FROM vehicles v JOIN vehicle_recall_checks c ON c.vehicle_id=v.id`).Scan(&trim, &checkTrim, &checkVIN, &matchVersion); err != nil {
		t.Fatal(err)
	}
	if trim != "" || checkTrim != "" || checkVIN != "" || matchVersion != 0 {
		t.Fatalf("defaults = trim %q check trim %q check vin %q version %d, want empty and 0", trim, checkTrim, checkVIN, matchVersion)
	}

	applyDown(t, ctx, connection, version)
	assertColumns(t, ctx, connection, "vehicles", nil, []string{"trim"})
	assertColumns(t, ctx, connection, "vehicle_recall_checks", nil, []string{"trim", "vin", "match_version"})
	assertPreserved()
}

// assertColumns checks that each present column exists on table and each
// absent column does not.
func assertColumns(t *testing.T, ctx context.Context, connection *pgx.Conn, table string, present, absent []string) {
	t.Helper()
	check := func(columns []string, want bool) {
		for _, column := range columns {
			var exists bool
			if err := connection.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1 AND column_name=$2)`, table, column).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists != want {
				t.Fatalf("%s.%s exists = %v, want %v", table, column, exists, want)
			}
		}
	}
	check(present, true)
	check(absent, false)
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
