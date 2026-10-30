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

func TestReminderSnoozeRoundTripAndMatchingRecord(t *testing.T) {
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

	userID, vehicleID, otherVehicleID := uuid.New(), uuid.New(), uuid.New()
	oilID, tireID := uuid.New(), uuid.New()
	reminderID := uuid.New()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,oauth_provider,oauth_subject,email) VALUES($1,'test','snooze','snooze@example.com')`, []any{userID}},
		{`INSERT INTO vehicles(id,nickname,current_odometer_miles) VALUES($1,'Wagon',10000)`, []any{vehicleID}},
		{`INSERT INTO vehicles(id,nickname) VALUES($1,'Other')`, []any{otherVehicleID}},
		{`INSERT INTO service_types(id,name) VALUES($1,'Snooze oil')`, []any{oilID}},
		{`INSERT INTO service_types(id,name) VALUES($1,'Snooze tires')`, []any{tireID}},
		{`INSERT INTO reminders(id,vehicle_id,service_type_id,interval_months) VALUES($1,$2,$3,6)`, []any{reminderID, vehicleID, oilID}},
	} {
		if _, err = connection.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	until := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	miles := int64(15500)
	updated := time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC)
	if err = store.SetReminderSnooze(ctx, otherVehicleID, reminderID, &until, &miles, updated); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("wrong vehicle err=%v", err)
	}
	if err = store.SetReminderSnooze(ctx, vehicleID, reminderID, &until, &miles, updated); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListReminders(ctx, &vehicleID, true)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if rows[0].SnoozedUntilOn == nil || rows[0].SnoozedUntilOn.Format(time.DateOnly) != "2026-11-13" || rows[0].SnoozedUntilMiles == nil || *rows[0].SnoozedUntilMiles != 15500 {
		t.Fatalf("stored snooze %+v", rows[0])
	}
	if _, err = connection.Exec(ctx, `UPDATE reminders SET snoozed_until_miles=-1 WHERE id=$1`, reminderID); err == nil {
		t.Fatal("negative snooze mileage was accepted")
	}

	now := updated.Add(time.Hour)
	if _, err = store.CreateRecord(ctx, model.Record{
		ID: uuid.New(), VehicleID: vehicleID, ServiceTypeID: tireID, CreatedBy: userID,
		OccurredOn: now, CreatedAt: now, UpdatedAt: now,
	}, nil); err != nil {
		t.Fatal(err)
	}
	rows, _ = store.ListReminders(ctx, &vehicleID, true)
	if len(rows) != 1 || rows[0].SnoozedUntilOn == nil || rows[0].SnoozedUntilMiles == nil {
		t.Fatalf("unrelated record cleared snooze %+v", rows)
	}
	if _, err = store.CreateRecord(ctx, model.Record{
		ID: uuid.New(), VehicleID: vehicleID, ServiceTypeID: oilID, CreatedBy: userID,
		OccurredOn: now, CreatedAt: now, UpdatedAt: now,
	}, nil); err != nil {
		t.Fatal(err)
	}
	rows, _ = store.ListReminders(ctx, &vehicleID, true)
	if len(rows) != 1 || rows[0].SnoozedUntilOn != nil || rows[0].SnoozedUntilMiles != nil {
		t.Fatalf("matching record left snooze %+v", rows)
	}

	if err = store.SetReminderSnooze(ctx, vehicleID, reminderID, &until, nil, now); err != nil {
		t.Fatal(err)
	}
	saved, err := store.UpsertReminder(ctx, model.Reminder{
		ID: uuid.New(), VehicleID: vehicleID, ServiceTypeID: oilID, IntervalMonths: months(3),
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ = store.ListReminders(ctx, &vehicleID, true)
	if saved.IntervalMonths == nil || *saved.IntervalMonths != 3 || len(rows) != 1 || rows[0].SnoozedUntilOn == nil || rows[0].SnoozedUntilOn.Format(time.DateOnly) != "2026-11-13" || rows[0].SnoozedUntilMiles != nil {
		t.Fatalf("upsert cleared snooze saved=%+v row=%+v", saved, rows)
	}
	if err = store.SetReminderSnooze(ctx, vehicleID, reminderID, nil, nil, now); err != nil {
		t.Fatal(err)
	}
	rows, _ = store.ListReminders(ctx, &vehicleID, true)
	if len(rows) != 1 || rows[0].SnoozedUntilOn != nil || rows[0].SnoozedUntilMiles != nil {
		t.Fatalf("explicit clear left %+v", rows)
	}
}

func months(v int) *int { return &v }
