package repository_test

import (
	"context"
	"errors"
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

func TestVehicleUpdatePostgresIntegration(t *testing.T) {
	baseURL := os.Getenv("CARMA_TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("set CARMA_TEST_DATABASE_URL to run PostgreSQL vehicle tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(context.Background()) }()
	schema := "carma_vehicle_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) }()
	scopedURL, err := withSearchPath(baseURL, schema)
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
	store, err := repository.NewPostgres(ctx, scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	userID, vehicleID, serviceTypeID := uuid.New(), uuid.New(), uuid.New()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,oauth_provider,oauth_subject,email) VALUES($1,'test','vehicle','driver@example.com')`, []any{userID}},
		{`INSERT INTO vehicles(id,nickname,photo_key) VALUES($1,'Original','old.jpg')`, []any{vehicleID}},
		{`INSERT INTO service_types(id,name) VALUES($1,'Test service')`, []any{serviceTypeID}},
		{`INSERT INTO records(vehicle_id,service_type_id,occurred_on,created_by,odometer_miles) VALUES($1,$2,current_date,$3,1400)`, []any{vehicleID, serviceTypeID, userID}},
		{`INSERT INTO reminders(vehicle_id,service_type_id,interval_miles,starting_odometer_pending) VALUES($1,$2,5000,true)`, []any{vehicleID, serviceTypeID}},
	} {
		if _, err = connection.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	vehicle, err := store.GetVehicle(ctx, vehicleID)
	if err != nil {
		t.Fatal(err)
	}
	current := int64(1200)
	vehicle.Nickname = "Updated"
	vehicle.PhotoKey = "new.jpg"
	vehicle.CurrentOdometer = &current
	vehicle.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)

	t.Run("reminder failure rolls back vehicle update", func(t *testing.T) {
		if _, err := connection.Exec(ctx, `
			CREATE FUNCTION reject_reminder_update() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'test reminder failure'; END $$;
			CREATE TRIGGER reject_reminder_update BEFORE UPDATE ON reminders
			FOR EACH ROW EXECUTE FUNCTION reject_reminder_update()`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpdateVehicle(ctx, vehicle); err == nil {
			t.Fatal("expected reminder initialization failure")
		}
		persisted, err := store.GetVehicle(ctx, vehicleID)
		if err != nil || persisted.PhotoKey != "old.jpg" || persisted.CurrentOdometer != nil || persisted.Nickname != "Original" {
			t.Fatalf("failed transaction changed vehicle: %+v err=%v", persisted, err)
		}
		if _, err := connection.Exec(ctx, `DROP TRIGGER reject_reminder_update ON reminders`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("commit failure rolls back vehicle and reminder", func(t *testing.T) {
		if _, err := connection.Exec(ctx, `
			CREATE FUNCTION reject_vehicle_commit() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'test commit failure'; END $$;
			CREATE CONSTRAINT TRIGGER reject_vehicle_commit AFTER UPDATE ON vehicles
			DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_vehicle_commit()`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpdateVehicle(ctx, vehicle); err == nil {
			t.Fatal("expected commit failure")
		}
		persisted, err := store.GetVehicle(ctx, vehicleID)
		if err != nil || persisted.PhotoKey != "old.jpg" || persisted.CurrentOdometer != nil {
			t.Fatalf("failed commit changed vehicle: %+v err=%v", persisted, err)
		}
		var pending bool
		var baseline *int64
		if err := connection.QueryRow(ctx, `SELECT starting_odometer_pending,starting_odometer_miles FROM reminders WHERE vehicle_id=$1`, vehicleID).Scan(&pending, &baseline); err != nil {
			t.Fatal(err)
		}
		if !pending || baseline != nil {
			t.Fatalf("failed commit changed reminder: pending=%t baseline=%v", pending, baseline)
		}
		if _, err := connection.Exec(ctx, `DROP TRIGGER reject_vehicle_commit ON vehicles`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("returns persisted photo and effective odometer", func(t *testing.T) {
		updated, err := store.UpdateVehicle(ctx, vehicle)
		if err != nil {
			t.Fatal(err)
		}
		if updated.PhotoKey != "new.jpg" || updated.CurrentOdometer == nil || *updated.CurrentOdometer != 1200 || updated.LatestOdometer == nil || *updated.LatestOdometer != 1400 || updated.Nickname != "Updated" || !updated.CreatedAt.Equal(vehicle.CreatedAt) || !updated.UpdatedAt.Equal(vehicle.UpdatedAt) {
			t.Fatalf("incomplete returned vehicle: %+v", updated)
		}
		var pending bool
		var baseline *int64
		if err := connection.QueryRow(ctx, `SELECT starting_odometer_pending,starting_odometer_miles FROM reminders WHERE vehicle_id=$1`, vehicleID).Scan(&pending, &baseline); err != nil {
			t.Fatal(err)
		}
		if pending || baseline == nil || *baseline != 1400 {
			t.Fatalf("reminder baseline not initialized: pending=%t baseline=%v", pending, baseline)
		}
	})

	t.Run("canceled update preserves saved photo", func(t *testing.T) {
		canceled, stop := context.WithCancel(ctx)
		stop()
		vehicle.PhotoKey = "canceled.jpg"
		if _, err := store.UpdateVehicle(canceled, vehicle); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled update error=%v", err)
		}
		persisted, err := store.GetVehicle(ctx, vehicleID)
		if err != nil || persisted.PhotoKey != "new.jpg" {
			t.Fatalf("canceled update changed vehicle: %+v err=%v", persisted, err)
		}
	})

	t.Run("missing vehicle", func(t *testing.T) {
		vehicle.ID = uuid.New()
		if _, err := store.UpdateVehicle(ctx, vehicle); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("missing vehicle error=%v", err)
		}
	})
}
