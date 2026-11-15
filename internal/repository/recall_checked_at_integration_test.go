package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/testdb"
	"github.com/google/uuid"
)

// A successful empty check followed by a trim or VIN edit whose immediate
// lookup fails must not keep presenting the old clean result: RecallCheckedAt
// is nil, so the vehicle page says "Not checked yet" rather than "No open
// recalls".
func TestRecallCheckedAtIgnoresEmptyCheckAfterTrimOrVINEdit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store := testdb.Store(t)
	year := 2024
	now := time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)
	vehicle, err := store.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: "Benz", Year: &year, Make: "Mercedes-Benz", Model: "E-Class", Trim: "E450 4MATIC", CreatedAt: now, UpdatedAt: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveRecallCheck(ctx, vehicle, nil, now); err != nil {
		t.Fatal(err)
	}
	if checkedAt, err := store.RecallCheckedAt(ctx, vehicle.ID); err != nil || checkedAt == nil || !checkedAt.Equal(now) {
		t.Fatalf("checked at after an empty check = %v err=%v", checkedAt, err)
	}
	edits := []struct {
		name string
		edit func(*model.Vehicle)
	}{
		{"trim", func(v *model.Vehicle) { v.Trim = "E 450 4MATIC" }},
		{"vin", func(v *model.Vehicle) { v.VIN = "W1KLF6BB7VA343747" }},
	}
	for _, tc := range edits {
		edited := vehicle
		tc.edit(&edited)
		edited.UpdatedAt = now.Add(time.Minute)
		if _, _, err = store.UpdateVehicle(ctx, edited); err != nil {
			t.Fatal(err)
		}
		// No SaveRecallCheck: the lookup for the edited vehicle failed.
		if checkedAt, err := store.RecallCheckedAt(ctx, vehicle.ID); err != nil || checkedAt != nil {
			t.Fatalf("%s edit: checked at = %v err=%v, want nil", tc.name, checkedAt, err)
		}
		if recalls, err := store.ListRecalls(ctx, &vehicle.ID, false); err != nil || len(recalls) != 0 {
			t.Fatalf("%s edit: recalls = %+v err=%v", tc.name, recalls, err)
		}
		// Putting the vehicle back makes the original empty check current again.
		if _, _, err = store.UpdateVehicle(ctx, vehicle); err != nil {
			t.Fatal(err)
		}
		if checkedAt, err := store.RecallCheckedAt(ctx, vehicle.ID); err != nil || checkedAt == nil || !checkedAt.Equal(now) {
			t.Fatalf("%s restored: checked at = %v err=%v, want the original check", tc.name, checkedAt, err)
		}
	}
}
