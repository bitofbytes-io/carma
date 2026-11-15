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

func TestSaveRecallCheckRefusesAnOlderLookupPostgresIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store := testdb.Store(t)
	year := 2024
	start := time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)
	vehicle, err := store.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: "Benz", Year: &year, Make: "Mercedes-Benz", Model: "E-Class", CreatedAt: start, UpdatedAt: start}, nil)
	if err != nil {
		t.Fatal(err)
	}
	campaign := func(number string) []model.Recall { return []model.Recall{{CampaignNumber: number}} }
	saved := func() []string {
		t.Helper()
		recalls, err := store.ListRecalls(ctx, &vehicle.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		var numbers []string
		for _, recall := range recalls {
			numbers = append(numbers, recall.CampaignNumber)
		}
		return numbers
	}

	// Two checks overlap: the older lookup (started first) is saved last.
	olderStarted, newerStarted := start.Add(time.Minute), start.Add(2*time.Minute)
	clock := func(at time.Time) func() time.Time { return func() time.Time { return at } }
	if _, err = store.SaveRecallCheck(ctx, vehicle, campaign("24V000002"), newerStarted, clock(start.Add(3*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveRecallCheck(ctx, vehicle, campaign("24V000001"), olderStarted, clock(start.Add(4*time.Minute))); !errors.Is(err, repository.ErrStaleCheck) {
		t.Fatalf("older lookup err = %v, want ErrStaleCheck", err)
	}
	if numbers := saved(); len(numbers) != 1 || numbers[0] != "24V000002" {
		t.Fatalf("campaigns after the older lookup = %v, want only the newer check's", numbers)
	}
	checkedAt, err := store.RecallCheckedAt(ctx, vehicle.ID)
	if err != nil || checkedAt == nil || !checkedAt.Equal(start.Add(3*time.Minute)) {
		t.Fatalf("checked at after the refused save = %v err=%v", checkedAt, err)
	}

	// The time recorded is the one at save, taken after the vehicle was locked.
	if _, err = store.SaveRecallCheck(ctx, vehicle, campaign("24V000003"), start.Add(5*time.Minute), clock(start.Add(6*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if checkedAt, err = store.RecallCheckedAt(ctx, vehicle.ID); err != nil || checkedAt == nil || !checkedAt.Equal(start.Add(6*time.Minute)) {
		t.Fatalf("checked at = %v err=%v, want the save time", checkedAt, err)
	}
	// A lookup that began before that save is refused even if it began after
	// the previous one.
	if _, err = store.SaveRecallCheck(ctx, vehicle, campaign("24V000004"), start.Add(5*time.Minute+30*time.Second), clock(start.Add(7*time.Minute))); !errors.Is(err, repository.ErrStaleCheck) {
		t.Fatalf("lookup begun before the last save err = %v, want ErrStaleCheck", err)
	}
}
