package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/internal/testdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestVehicleUpdatePostgresIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	scopedURL := testdb.URL(t)
	connection, err := pgx.Connect(ctx, scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close(context.Background()) }()
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
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := connection.Exec(cleanupCtx, `DROP TRIGGER reject_reminder_update ON reminders; DROP FUNCTION reject_reminder_update()`); err != nil {
				t.Errorf("remove reminder failure trigger: %v", err)
			}
		})
		if _, _, err := store.UpdateVehicle(ctx, vehicle); err == nil {
			t.Fatal("expected reminder initialization failure")
		}
		persisted, err := store.GetVehicle(ctx, vehicleID)
		if err != nil || persisted.PhotoKey != "old.jpg" || persisted.CurrentOdometer != nil || persisted.Nickname != "Original" {
			t.Fatalf("failed transaction changed vehicle: %+v err=%v", persisted, err)
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
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := connection.Exec(cleanupCtx, `DROP TRIGGER reject_vehicle_commit ON vehicles; DROP FUNCTION reject_vehicle_commit()`); err != nil {
				t.Errorf("remove vehicle commit failure trigger: %v", err)
			}
		})
		if _, _, err := store.UpdateVehicle(ctx, vehicle); err == nil {
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
	})

	t.Run("returns persisted photo and effective odometer", func(t *testing.T) {
		updated, replaced, err := store.UpdateVehicle(ctx, vehicle)
		if err != nil {
			t.Fatal(err)
		}
		if replaced != "old.jpg" {
			t.Fatalf("replaced photo key = %q, want old.jpg", replaced)
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

	t.Run("update without upload keeps the current photo", func(t *testing.T) {
		// A form loaded before another edit replaced the photo must not write
		// the stale key back (CAR-6).
		if _, err := connection.Exec(ctx, `UPDATE vehicles SET photo_key='concurrent.jpg' WHERE id=$1`, vehicleID); err != nil {
			t.Fatal(err)
		}
		withoutUpload := vehicle
		withoutUpload.PhotoKey = ""
		withoutUpload.Nickname = "Renamed"
		updated, replaced, err := store.UpdateVehicle(ctx, withoutUpload)
		if err != nil {
			t.Fatal(err)
		}
		if updated.PhotoKey != "concurrent.jpg" || replaced != "" || updated.Nickname != "Renamed" {
			t.Fatalf("update without upload photo=%q replaced=%q nickname=%q", updated.PhotoKey, replaced, updated.Nickname)
		}
		if _, err := connection.Exec(ctx, `UPDATE vehicles SET photo_key='new.jpg' WHERE id=$1`, vehicleID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("resaving the current photo key replaces nothing", func(t *testing.T) {
		updated, replaced, err := store.UpdateVehicle(ctx, vehicle)
		if err != nil || updated.PhotoKey != "new.jpg" || replaced != "" {
			t.Fatalf("photo=%q replaced=%q err=%v", updated.PhotoKey, replaced, err)
		}
	})

	t.Run("canceled update preserves saved photo", func(t *testing.T) {
		canceled, stop := context.WithCancel(ctx)
		stop()
		vehicle.PhotoKey = "canceled.jpg"
		if _, _, err := store.UpdateVehicle(canceled, vehicle); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled update error=%v", err)
		}
		persisted, err := store.GetVehicle(ctx, vehicleID)
		if err != nil || persisted.PhotoKey != "new.jpg" {
			t.Fatalf("canceled update changed vehicle: %+v err=%v", persisted, err)
		}
	})

	t.Run("missing vehicle", func(t *testing.T) {
		vehicle.ID = uuid.New()
		if _, _, err := store.UpdateVehicle(ctx, vehicle); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("missing vehicle error=%v", err)
		}
	})
}

func TestCreateVehicleWithRemindersIsAtomicPostgresIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store := testdb.Store(t)
	types, err := store.ListServiceTypes(ctx)
	if err != nil || len(types) == 0 {
		t.Fatalf("service types=%v err=%v", types, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	months := 6
	starting := int64(1200)
	vehicle := model.Vehicle{ID: uuid.New(), Nickname: "Starter", CurrentOdometer: &starting, CreatedAt: now, UpdatedAt: now}
	valid := model.Reminder{ID: uuid.New(), ServiceTypeID: types[0].ID, IntervalMonths: &months, StartingOdometer: &starting, Enabled: true, CreatedAt: now, UpdatedAt: now}
	invalid := valid
	invalid.ID, invalid.ServiceTypeID = uuid.New(), uuid.New()
	if _, err := store.CreateVehicle(ctx, vehicle, []model.Reminder{valid, invalid}); err == nil {
		t.Fatal("reminder with an unknown service type was accepted")
	}
	if _, err := store.GetVehicle(ctx, vehicle.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("failed reminder insert left the vehicle behind: err=%v", err)
	}

	created, err := store.CreateVehicle(ctx, vehicle, []model.Reminder{valid})
	if err != nil || created.ID != vehicle.ID {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	rows, err := store.ListReminders(ctx, &vehicle.ID, true)
	if err != nil || len(rows) != 1 || rows[0].VehicleID != vehicle.ID || rows[0].StartingOdometer == nil || *rows[0].StartingOdometer != 1200 {
		t.Fatalf("reminders=%+v err=%v", rows, err)
	}
}
