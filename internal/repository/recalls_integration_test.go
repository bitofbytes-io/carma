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
)

func TestRecallsPostgresIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store := testdb.Store(t)
	var _ repository.RecallCheckStore = store

	year := 2019
	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	newVehicle := func(nickname, vehicleMake, vehicleModel string, year *int) model.Vehicle {
		t.Helper()
		vehicle, err := store.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: nickname, Year: year, Make: vehicleMake, Model: vehicleModel, CreatedAt: now, UpdatedAt: now}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return vehicle
	}
	outback := newVehicle("Outback", "Subaru", "Outback", &year)
	newVehicle("No year", "Subaru", "Outback", nil)
	newVehicle("No model", "Subaru", " ", &year)
	archived := newVehicle("Archived", "Honda", "Civic", &year)
	if err := store.ArchiveVehicle(ctx, archived.ID); err != nil {
		t.Fatal(err)
	}
	dueIDs := func(staleBefore time.Time) []uuid.UUID {
		t.Helper()
		due, err := store.ListRecallCheckDue(ctx, staleBefore)
		if err != nil {
			t.Fatal(err)
		}
		var ids []uuid.UUID
		for _, vehicle := range due {
			ids = append(ids, vehicle.ID)
		}
		return ids
	}
	if ids := dueIDs(now); len(ids) != 1 || ids[0] != outback.ID {
		t.Fatalf("due before any check = %v, want only %s", ids, outback.ID)
	}

	received := time.Date(2019, time.June, 26, 0, 0, 0, 0, time.UTC)
	recalls := []model.Recall{
		{CampaignNumber: "19V493000", Component: "STRUCTURE", Summary: "Spot welds.", ReportReceivedOn: &received, ParkIt: true},
		{CampaignNumber: "21V587000", Component: "FUEL PUMP"},
	}
	if added, err := store.SaveRecallCheck(ctx, outback, recalls, now); err != nil || added != 2 {
		t.Fatalf("first save added=%d err=%v", added, err)
	}
	if ids := dueIDs(now); len(ids) != 0 {
		t.Fatalf("due right after a check = %v", ids)
	}
	if ids := dueIDs(now.Add(time.Second)); len(ids) != 1 {
		t.Fatalf("due once the check is stale = %v", ids)
	}
	open, err := store.ListRecalls(ctx, nil, false)
	if err != nil || len(open) != 2 || open[0].CampaignNumber != "19V493000" || open[0].VehicleName != "Outback" || open[0].ReportReceivedOn == nil || !open[0].ReportReceivedOn.Equal(received) || !open[0].ParkIt || open[1].ReportReceivedOn != nil {
		t.Fatalf("open recalls = %+v err=%v", open, err)
	}

	if err = store.SetRecallDismissed(ctx, outback.ID, "19V493000", &now); err != nil {
		t.Fatal(err)
	}
	if err = store.SetRecallDismissed(ctx, outback.ID, "00V000000", &now); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("dismiss unknown campaign err=%v", err)
	}
	if open, err = store.ListRecalls(ctx, &outback.ID, false); err != nil || len(open) != 1 || open[0].CampaignNumber != "21V587000" {
		t.Fatalf("open after dismiss = %+v err=%v", open, err)
	}

	// A refresh keeps the dismissal and first-seen time of a campaign still
	// listed, drops one NHTSA no longer lists, and counts only new ones.
	refreshed := []model.Recall{{CampaignNumber: "19V493000", Component: "STRUCTURE (UPDATED)"}, {CampaignNumber: "24V100000", Component: "BRAKES"}}
	if added, err := store.SaveRecallCheck(ctx, outback, refreshed, now.Add(7*24*time.Hour)); err != nil || added != 1 {
		t.Fatalf("refresh added=%d err=%v", added, err)
	}
	all, err := store.ListRecalls(ctx, &outback.ID, true)
	if err != nil || len(all) != 2 {
		t.Fatalf("all after refresh = %+v err=%v", all, err)
	}
	for _, recall := range all {
		switch recall.CampaignNumber {
		case "19V493000":
			if recall.DismissedAt == nil || recall.Component != "STRUCTURE (UPDATED)" || !recall.FirstSeenAt.Equal(now) {
				t.Fatalf("kept campaign = %+v", recall)
			}
		case "24V100000":
			if recall.DismissedAt != nil {
				t.Fatalf("new campaign = %+v", recall)
			}
		default:
			t.Fatalf("unexpected campaign %+v", recall)
		}
	}
	if err = store.SetRecallDismissed(ctx, outback.ID, "19V493000", nil); err != nil {
		t.Fatal(err)
	}
	if open, err = store.ListRecalls(ctx, nil, false); err != nil || len(open) != 2 {
		t.Fatalf("open after restore = %+v err=%v", open, err)
	}

	// Editing the model hides the old campaigns and makes the vehicle due again.
	outback.Model = "Forester"
	if _, _, err = store.UpdateVehicle(ctx, outback); err != nil {
		t.Fatal(err)
	}
	if open, err = store.ListRecalls(ctx, nil, false); err != nil || len(open) != 0 {
		t.Fatalf("recalls for the old model still listed: %+v err=%v", open, err)
	}
	if ids := dueIDs(now); len(ids) != 1 || ids[0] != outback.ID {
		t.Fatalf("due after model change = %v", ids)
	}
	if added, err := store.SaveRecallCheck(ctx, outback, nil, now.Add(8*24*time.Hour)); err != nil || added != 0 {
		t.Fatalf("empty save added=%d err=%v", added, err)
	}
	if all, err = store.ListRecalls(ctx, &outback.ID, true); err != nil || len(all) != 0 {
		t.Fatalf("recalls after an empty check = %+v err=%v", all, err)
	}

	unlock, acquired, err := store.TryRecallCheckLock(ctx)
	if err != nil || !acquired {
		t.Fatalf("acquired=%v err=%v", acquired, err)
	}
	if _, again, err := store.TryRecallCheckLock(ctx); err != nil || again {
		t.Fatalf("second lock acquired=%v err=%v", again, err)
	}
	if err = unlock(ctx); err != nil {
		t.Fatal(err)
	}
}
