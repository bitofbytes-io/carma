package repository_test

import (
	"slices"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

func TestListVendorsDistinctAndMostRecentFirst(t *testing.T) {
	ctx, postgres := newPostgresIntegrationStore(t)
	if vendors, err := postgres.ListVendors(ctx); err != nil || len(vendors) != 0 {
		t.Fatalf("empty household vendors=%v err=%v", vendors, err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	user := model.User{ID: uuid.New(), OAuthProvider: "google", OAuthSubject: "vendors", Email: "vendors@example.com", DisplayName: "Driver", CreatedAt: now, UpdatedAt: now, LastLoginAt: now}
	car := model.Vehicle{ID: uuid.New(), Nickname: "Car", CreatedAt: now, UpdatedAt: now}
	truck := model.Vehicle{ID: uuid.New(), Nickname: "Truck", CreatedAt: now, UpdatedAt: now}
	oil := model.ServiceType{ID: uuid.New(), Name: "Vendor oil", CreatedAt: now}
	tires := model.ServiceType{ID: uuid.New(), Name: "Vendor tires", CreatedAt: now}
	if _, err := postgres.UpsertUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	for _, vehicle := range []model.Vehicle{car, truck} {
		if _, err := postgres.CreateVehicle(ctx, vehicle, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, serviceType := range []model.ServiceType{oil, tires} {
		if _, err := postgres.CreateServiceType(ctx, serviceType); err != nil {
			t.Fatal(err)
		}
	}

	date := func(year, month, day int) time.Time {
		return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}
	record := func(vehicle model.Vehicle, serviceType model.ServiceType, occurred, created time.Time, vendor string) model.Record {
		return model.Record{
			ID: uuid.New(), VehicleID: vehicle.ID, ServiceTypeID: serviceType.ID, CreatedBy: user.ID,
			OccurredOn: occurred, Vendor: vendor, CreatedAt: created, UpdatedAt: created,
		}
	}
	// Same shop logged twice collapses to its latest service date. A different
	// spelling stays distinct. An older service logged later does not outrank
	// a newer service date. Blank names are not suggestions.
	rows := []model.Record{
		record(car, oil, date(2026, 1, 10), now, "Jiffy Lube"),
		record(car, oil, date(2026, 7, 1), now.Add(time.Hour), "Jiffy Lube"),
		record(truck, tires, date(2026, 6, 1), now.Add(2*time.Hour), "jiffy lube"),
		record(car, tires, date(2026, 7, 1), now.Add(3*time.Hour), "Tire Barn"),
		record(truck, oil, date(2024, 1, 1), now.Add(48*time.Hour), "Old Shop"),
		record(car, oil, date(2026, 8, 1), now, ""),
		record(car, oil, date(2026, 8, 2), now, "   "),
		record(truck, tires, date(2026, 5, 1), now, "Tire Barn"),
	}
	for _, row := range rows {
		if _, err := postgres.CreateRecord(ctx, row, nil); err != nil {
			t.Fatal(err)
		}
	}

	vendors, err := postgres.ListVendors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Tire Barn", "Jiffy Lube", "jiffy lube", "Old Shop"}
	if !slices.Equal(vendors, want) {
		t.Fatalf("vendors=%q, want %q", vendors, want)
	}
}
