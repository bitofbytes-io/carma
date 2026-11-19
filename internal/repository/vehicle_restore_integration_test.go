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

func TestRestoreVehiclePostgresIntegration(t *testing.T) {
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
		{`INSERT INTO users(id,oauth_provider,oauth_subject,email) VALUES($1,'test','restore','driver@example.com')`, []any{userID}},
		{`INSERT INTO vehicles(id,nickname) VALUES($1,'Sold')`, []any{vehicleID}},
		{`INSERT INTO service_types(id,name) VALUES($1,'Test service')`, []any{serviceTypeID}},
		{`INSERT INTO records(vehicle_id,service_type_id,occurred_on,created_by,odometer_miles) VALUES($1,$2,current_date,$3,1400)`, []any{vehicleID, serviceTypeID, userID}},
		{`INSERT INTO reminders(vehicle_id,service_type_id,interval_miles,starting_odometer_pending) VALUES($1,$2,5000,true)`, []any{vehicleID, serviceTypeID}},
	} {
		if _, err = connection.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.ArchiveVehicle(ctx, vehicleID); err != nil {
		t.Fatal(err)
	}
	archived, err := store.GetVehicle(ctx, vehicleID)
	if err != nil || archived.ArchivedAt == nil {
		t.Fatalf("vehicle not archived: %+v err=%v", archived, err)
	}

	if err = store.RestoreVehicle(ctx, vehicleID); err != nil {
		t.Fatal(err)
	}
	restored, err := store.GetVehicle(ctx, vehicleID)
	if err != nil || restored.ArchivedAt != nil || !restored.UpdatedAt.After(archived.UpdatedAt) {
		t.Fatalf("vehicle not restored: %+v err=%v", restored, err)
	}
	if garage, err := store.ListVehicles(ctx, false); err != nil || len(garage) != 1 || garage[0].ID != vehicleID {
		t.Fatalf("garage = %+v err=%v", garage, err)
	}
	if archivedList, err := store.ListVehicles(ctx, true); err != nil || len(archivedList) != 0 {
		t.Fatalf("archived = %+v err=%v", archivedList, err)
	}
	if records, err := store.ListRecords(ctx, model.RecordQuery{VehicleID: &vehicleID}); err != nil || len(records) != 1 {
		t.Fatalf("records = %+v err=%v", records, err)
	}
	if reminders, err := store.ListReminders(ctx, &vehicleID, true); err != nil || len(reminders) != 1 {
		t.Fatalf("reminders = %+v err=%v", reminders, err)
	}

	t.Run("restoring an active vehicle changes nothing", func(t *testing.T) {
		if err := store.RestoreVehicle(ctx, vehicleID); err != nil {
			t.Fatal(err)
		}
		again, err := store.GetVehicle(ctx, vehicleID)
		if err != nil || again.ArchivedAt != nil || !again.UpdatedAt.Equal(restored.UpdatedAt) {
			t.Fatalf("no-op restore changed vehicle: %+v err=%v", again, err)
		}
	})
	t.Run("unknown vehicle", func(t *testing.T) {
		if err := store.RestoreVehicle(ctx, uuid.New()); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}
