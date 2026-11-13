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

func TestVehicleTrimAndRecallCheckStatusPostgresIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	databaseURL := testdb.URL(t)
	store, err := repository.NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	db, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(context.Background()) })
	year := 2024
	now := time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)
	vehicle, err := store.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: "Benz", Year: &year, Make: "Mercedes-Benz", Model: "E-Class", Trim: "E450 4MATIC", CreatedAt: now, UpdatedAt: now}, nil)
	if err != nil || vehicle.Trim != "E450 4MATIC" {
		t.Fatalf("created vehicle = %+v err=%v", vehicle, err)
	}
	if got, err := store.GetVehicle(ctx, vehicle.ID); err != nil || got.Trim != "E450 4MATIC" {
		t.Fatalf("loaded vehicle trim = %q err=%v", got.Trim, err)
	}
	if listed, err := store.ListVehicles(ctx, false); err != nil || len(listed) != 1 || listed[0].Trim != "E450 4MATIC" {
		t.Fatalf("listed vehicles = %+v err=%v", listed, err)
	}
	checkedAt, err := store.RecallCheckedAt(ctx, vehicle.ID)
	if err != nil || checkedAt != nil {
		t.Fatalf("checked before any check: %v err=%v", checkedAt, err)
	}

	due := func() []model.Vehicle {
		t.Helper()
		vehicles, err := store.ListRecallCheckDue(ctx, now.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return vehicles
	}
	if vehicles := due(); len(vehicles) != 1 || vehicles[0].Trim != "E450 4MATIC" {
		t.Fatalf("due before any check = %+v", vehicles)
	}
	if _, err = store.SaveRecallCheck(ctx, vehicle, []model.Recall{{CampaignNumber: "24V224000"}}, now); err != nil {
		t.Fatal(err)
	}
	if vehicles := due(); len(vehicles) != 0 {
		t.Fatalf("due right after a check = %+v", vehicles)
	}
	if checkedAt, err = store.RecallCheckedAt(ctx, vehicle.ID); err != nil || checkedAt == nil || !checkedAt.Equal(now) {
		t.Fatalf("checked at = %v err=%v", checkedAt, err)
	}

	// A trim change is due again, but keeps the recalls and check time shown.
	vehicle.Trim, vehicle.UpdatedAt = "E 450 4MATIC", now.Add(time.Minute)
	if _, _, err = store.UpdateVehicle(ctx, vehicle); err != nil {
		t.Fatal(err)
	}
	if vehicles := due(); len(vehicles) != 1 || vehicles[0].Trim != "E 450 4MATIC" {
		t.Fatalf("due after a trim edit = %+v", vehicles)
	}
	if recalls, err := store.ListRecalls(ctx, &vehicle.ID, false); err != nil || len(recalls) != 1 {
		t.Fatalf("recalls after a trim edit = %+v err=%v", recalls, err)
	}
	vehicle.Trim = "E450 4MATIC"
	if _, _, err = store.UpdateVehicle(ctx, vehicle); err != nil {
		t.Fatal(err)
	}
	if vehicles := due(); len(vehicles) != 0 {
		t.Fatalf("due after the trim is restored = %+v", vehicles)
	}

	// A VIN change is due again too, since the VIN supplies the trim when none
	// is saved.
	vehicle.VIN, vehicle.UpdatedAt = "W1KLF6BB7VA343747", now.Add(90*time.Second)
	if _, _, err = store.UpdateVehicle(ctx, vehicle); err != nil {
		t.Fatal(err)
	}
	if vehicles := due(); len(vehicles) != 1 || vehicles[0].VIN != "W1KLF6BB7VA343747" {
		t.Fatalf("due after a VIN edit = %+v", vehicles)
	}
	// A result looked up for the vehicle as it was before the edit is refused
	// instead of replacing the campaigns of the edited vehicle.
	staleVehicle := vehicle
	staleVehicle.VIN = ""
	if _, err = store.SaveRecallCheck(ctx, staleVehicle, []model.Recall{{CampaignNumber: "00V000000"}}, now.Add(2*time.Minute)); !errors.Is(err, repository.ErrStaleVehicle) {
		t.Fatalf("stale save err = %v", err)
	}
	if recalls, err := store.ListRecalls(ctx, &vehicle.ID, false); err != nil || len(recalls) != 1 || recalls[0].CampaignNumber != "24V224000" {
		t.Fatalf("recalls after a stale save = %+v err=%v", recalls, err)
	}
	if _, err = store.SaveRecallCheck(ctx, vehicle, []model.Recall{{CampaignNumber: "24V224000"}}, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if vehicles := due(); len(vehicles) != 0 {
		t.Fatalf("due after checking the new VIN = %+v", vehicles)
	}

	// Checks made before the current matching are redone once.
	if _, err = db.Exec(ctx, `UPDATE vehicle_recall_checks SET match_version=$1`, repository.RecallMatchVersion-1); err != nil {
		t.Fatal(err)
	}
	if vehicles := due(); len(vehicles) != 1 {
		t.Fatalf("due after older matching = %+v", vehicles)
	}

	// A changed model hides the check, so the page says it is not checked.
	vehicle.Model, vehicle.UpdatedAt = "CLS-Class", now.Add(2*time.Minute)
	if _, _, err = store.UpdateVehicle(ctx, vehicle); err != nil {
		t.Fatal(err)
	}
	if checkedAt, err = store.RecallCheckedAt(ctx, vehicle.ID); err != nil || checkedAt != nil {
		t.Fatalf("checked after the model changed: %v err=%v", checkedAt, err)
	}
}
