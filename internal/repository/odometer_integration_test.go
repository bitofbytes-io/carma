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

func TestAddOdometerReadingPostgresIntegration(t *testing.T) {
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
	userID, vehicleID, emptyVehicleID, serviceTypeID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,oauth_provider,oauth_subject,email) VALUES($1,'test','odometer','driver@example.com')`, []any{userID}},
		{`INSERT INTO vehicles(id,nickname,current_odometer_miles) VALUES($1,'Wagon',1000)`, []any{vehicleID}},
		{`INSERT INTO vehicles(id,nickname) VALUES($1,'New car')`, []any{emptyVehicleID}},
		{`INSERT INTO service_types(id,name) VALUES($1,'Test service')`, []any{serviceTypeID}},
		{`INSERT INTO records(vehicle_id,service_type_id,occurred_on,created_by,odometer_miles) VALUES($1,$2,current_date,$3,1400)`, []any{vehicleID, serviceTypeID, userID}},
		{`INSERT INTO reminders(vehicle_id,service_type_id,interval_miles,starting_odometer_pending) VALUES($1,$2,5000,true)`, []any{emptyVehicleID, serviceTypeID}},
	} {
		if _, err = connection.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	readOn := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	reading := func(vehicle uuid.UUID, miles int64) model.OdometerReading {
		return model.OdometerReading{ID: uuid.New(), VehicleID: vehicle, ReadOn: readOn, OdometerMiles: miles, CreatedBy: userID, CreatedAt: time.Now()}
	}
	countReadings := func(vehicle uuid.UUID) int {
		t.Helper()
		var count int
		if err := connection.QueryRow(ctx, `SELECT count(*) FROM odometer_readings WHERE vehicle_id=$1`, vehicle).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	t.Run("reading below record mileage needs confirmation", func(t *testing.T) {
		vehicle, err := store.AddOdometerReading(ctx, reading(vehicleID, 1300), false)
		if !errors.Is(err, repository.ErrOdometerBelowLatest) {
			t.Fatalf("error=%v, want ErrOdometerBelowLatest", err)
		}
		if vehicle.LatestOdometer == nil || *vehicle.LatestOdometer != 1400 {
			t.Fatalf("rejected reading returned latest=%v, want 1400", vehicle.LatestOdometer)
		}
		if n := countReadings(vehicleID); n != 0 {
			t.Fatalf("rejected reading stored %d rows", n)
		}
	})

	t.Run("higher reading is stored and raises the vehicle", func(t *testing.T) {
		vehicle, err := store.AddOdometerReading(ctx, reading(vehicleID, 1500), false)
		if err != nil {
			t.Fatal(err)
		}
		if vehicle.CurrentOdometer == nil || *vehicle.CurrentOdometer != 1500 || vehicle.LatestOdometer == nil || *vehicle.LatestOdometer != 1500 {
			t.Fatalf("vehicle after reading: current=%v latest=%v", vehicle.CurrentOdometer, vehicle.LatestOdometer)
		}
		var readDate time.Time
		var miles int64
		var createdBy uuid.UUID
		if err := connection.QueryRow(ctx, `SELECT read_on,odometer_miles,created_by FROM odometer_readings WHERE vehicle_id=$1`, vehicleID).Scan(&readDate, &miles, &createdBy); err != nil {
			t.Fatal(err)
		}
		if !readDate.Equal(readOn) || miles != 1500 || createdBy != userID {
			t.Fatalf("stored reading date=%v miles=%d created_by=%v", readDate, miles, createdBy)
		}
	})

	t.Run("equal reading needs no confirmation", func(t *testing.T) {
		if _, err := store.AddOdometerReading(ctx, reading(vehicleID, 1500), false); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("confirmed lower reading is kept without lowering the vehicle", func(t *testing.T) {
		vehicle, err := store.AddOdometerReading(ctx, reading(vehicleID, 900), true)
		if err != nil {
			t.Fatal(err)
		}
		if vehicle.CurrentOdometer == nil || *vehicle.CurrentOdometer != 1500 || vehicle.LatestOdometer == nil || *vehicle.LatestOdometer != 1500 {
			t.Fatalf("confirmed lower reading changed vehicle: current=%v latest=%v", vehicle.CurrentOdometer, vehicle.LatestOdometer)
		}
		if n := countReadings(vehicleID); n != 3 {
			t.Fatalf("history has %d readings, want 3", n)
		}
	})

	t.Run("first reading sets mileage and pending reminder baselines", func(t *testing.T) {
		vehicle, err := store.AddOdometerReading(ctx, reading(emptyVehicleID, 0), false)
		if err != nil {
			t.Fatal(err)
		}
		if vehicle.CurrentOdometer == nil || *vehicle.CurrentOdometer != 0 {
			t.Fatalf("first reading current=%v, want 0", vehicle.CurrentOdometer)
		}
		var pending bool
		var baseline *int64
		if err := connection.QueryRow(ctx, `SELECT starting_odometer_pending,starting_odometer_miles FROM reminders WHERE vehicle_id=$1`, emptyVehicleID).Scan(&pending, &baseline); err != nil {
			t.Fatal(err)
		}
		if pending || baseline == nil || *baseline != 0 {
			t.Fatalf("reminder baseline not initialized: pending=%t baseline=%v", pending, baseline)
		}
	})

	t.Run("missing vehicle", func(t *testing.T) {
		if _, err := store.AddOdometerReading(ctx, reading(uuid.New(), 10), false); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("missing vehicle error=%v", err)
		}
	})
}
