package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/database"
	"github.com/bitofbytes-io/carma/internal/testdb"
	"github.com/bitofbytes-io/carma/migrations"
	"github.com/jackc/pgx/v5"
)

func TestVehicleOdometerDateBackfillUsesHouseholdZone(t *testing.T) {
	ctx := context.Background()
	connection, err := pgx.Connect(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close(context.Background()) }()
	// The database session runs in UTC while the household is in New York.
	if _, err = connection.Exec(ctx, `SET TIME ZONE 'UTC'`); err != nil {
		t.Fatal(err)
	}
	down, err := migrations.FS.ReadFile("007_vehicle_odometer_date.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	// Mileage last edited at 11:30 p.m. on Jul 31 in New York, which is
	// already Aug 1 in UTC, with no reading or record to date it.
	var evening, unknown string
	if err = connection.QueryRow(ctx, `INSERT INTO vehicles(nickname,current_odometer_miles,updated_at)
		VALUES('Ranger',5000,'2026-07-31 23:30:00-04') RETURNING id::text`).Scan(&evening); err != nil {
		t.Fatal(err)
	}
	if err = connection.QueryRow(ctx, `INSERT INTO vehicles(nickname,updated_at)
		VALUES('No mileage','2026-07-31 23:30:00-04') RETURNING id::text`).Scan(&unknown); err != nil {
		t.Fatal(err)
	}

	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(ctx, connection, migrations.FS, newYork); err != nil {
		t.Fatalf("reapply 007: %v", err)
	}
	var on *string
	if err = connection.QueryRow(ctx, `SELECT current_odometer_on::text FROM vehicles WHERE id=$1`, evening).Scan(&on); err != nil {
		t.Fatal(err)
	}
	if on == nil || *on != "2026-07-31" {
		got := "NULL"
		if on != nil {
			got = *on
		}
		t.Fatalf("current_odometer_on=%s, want the household date 2026-07-31", got)
	}
	if err = connection.QueryRow(ctx, `SELECT current_odometer_on::text FROM vehicles WHERE id=$1`, unknown).Scan(&on); err != nil {
		t.Fatal(err)
	}
	if on != nil {
		t.Fatalf("vehicle without mileage got current_odometer_on=%s", *on)
	}
	// The setting is transaction-local and does not leak into the session.
	var leaked string
	if err = connection.QueryRow(ctx, `SELECT coalesce(current_setting($1, true), '')`, database.HouseholdTimezoneSetting).Scan(&leaked); err != nil || leaked != "" {
		t.Fatalf("household zone setting after migrate=%q err=%v", leaked, err)
	}
}
