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

func TestCreateRecordLeavesSnoozeWhenRecordIsNotLatest(t *testing.T) {
	ctx, store, connection := openSnoozeDB(t)
	userID, vehicleID, oilID, reminderID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,oauth_provider,oauth_subject,email) VALUES($1,'test','snooze-older','snooze-older@example.com')`, []any{userID}},
		{`INSERT INTO vehicles(id,nickname) VALUES($1,'Wagon')`, []any{vehicleID}},
		{`INSERT INTO service_types(id,name) VALUES($1,'Older oil')`, []any{oilID}},
		{`INSERT INTO reminders(id,vehicle_id,service_type_id,interval_months) VALUES($1,$2,$3,6)`, []any{reminderID, vehicleID, oilID}},
	} {
		if _, err := connection.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	at := func(month, day, hour int) time.Time {
		return time.Date(2026, time.Month(month), day, hour, 0, 0, 0, time.UTC)
	}
	baselineID := uuid.MustParse("00000000-0000-0000-0000-000000000005")
	if _, err := store.CreateRecord(ctx, model.Record{
		ID: baselineID, VehicleID: vehicleID, ServiceTypeID: oilID, CreatedBy: userID,
		OccurredOn: at(6, 1, 0), CreatedAt: at(6, 1, 12), UpdatedAt: at(6, 1, 12),
	}, nil); err != nil {
		t.Fatal(err)
	}
	until := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	miles := int64(15500)
	if err := store.SetReminderSnooze(ctx, vehicleID, reminderID, &until, &miles, at(7, 31, 15)); err != nil {
		t.Fatal(err)
	}

	// Earlier service date, with a later created_at, is not the baseline.
	if _, err := store.CreateRecord(ctx, model.Record{
		ID: uuid.MustParse("00000000-0000-0000-0000-000000000009"), VehicleID: vehicleID, ServiceTypeID: oilID, CreatedBy: userID,
		OccurredOn: at(5, 1, 0), CreatedAt: at(8, 2, 12), UpdatedAt: at(8, 2, 12),
	}, nil); err != nil {
		t.Fatal(err)
	}
	requireSnooze(t, "older date", store, ctx, vehicleID, oilID, "2026-11-13", &miles, &baselineID)

	// Same day as the baseline, recorded earlier that day.
	if _, err := store.CreateRecord(ctx, model.Record{
		ID: uuid.MustParse("00000000-0000-0000-0000-000000000003"), VehicleID: vehicleID, ServiceTypeID: oilID, CreatedBy: userID,
		OccurredOn: at(6, 1, 0), CreatedAt: at(6, 1, 11), UpdatedAt: at(6, 1, 11),
	}, nil); err != nil {
		t.Fatal(err)
	}
	requireSnooze(t, "earlier created_at", store, ctx, vehicleID, oilID, "2026-11-13", &miles, &baselineID)

	// Same day and created_at, but a smaller id, stays behind the baseline.
	if _, err := store.CreateRecord(ctx, model.Record{
		ID: uuid.MustParse("00000000-0000-0000-0000-000000000004"), VehicleID: vehicleID, ServiceTypeID: oilID, CreatedBy: userID,
		OccurredOn: at(6, 1, 0), CreatedAt: at(6, 1, 12), UpdatedAt: at(6, 1, 12),
	}, nil); err != nil {
		t.Fatal(err)
	}
	requireSnooze(t, "smaller id", store, ctx, vehicleID, oilID, "2026-11-13", &miles, &baselineID)

	// Same day and created_at with a greater id becomes the baseline and clears.
	latestID := uuid.MustParse("00000000-0000-0000-0000-000000000006")
	if _, err := store.CreateRecord(ctx, model.Record{
		ID: latestID, VehicleID: vehicleID, ServiceTypeID: oilID, CreatedBy: userID,
		OccurredOn: at(6, 1, 0), CreatedAt: at(6, 1, 12), UpdatedAt: at(6, 1, 12),
	}, nil); err != nil {
		t.Fatal(err)
	}
	requireSnooze(t, "greater id", store, ctx, vehicleID, oilID, "", nil, &latestID)
}

func TestUpdateRecordClearsSnoozeOnlyWhenRowBecomesBaseline(t *testing.T) {
	ctx, store, connection := openSnoozeDB(t)
	userID, vehicleID := uuid.New(), uuid.New()
	oilID, tireID := uuid.New(), uuid.New()
	oilReminderID, tireReminderID := uuid.New(), uuid.New()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,oauth_provider,oauth_subject,email) VALUES($1,'test','snooze-edit','snooze-edit@example.com')`, []any{userID}},
		{`INSERT INTO vehicles(id,nickname) VALUES($1,'Wagon')`, []any{vehicleID}},
		{`INSERT INTO service_types(id,name) VALUES($1,'Edit oil')`, []any{oilID}},
		{`INSERT INTO service_types(id,name) VALUES($1,'Edit tires')`, []any{tireID}},
		{`INSERT INTO reminders(id,vehicle_id,service_type_id,interval_months) VALUES($1,$2,$3,6)`, []any{oilReminderID, vehicleID, oilID}},
		{`INSERT INTO reminders(id,vehicle_id,service_type_id,interval_months) VALUES($1,$2,$3,6)`, []any{tireReminderID, vehicleID, tireID}},
	} {
		if _, err := connection.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	at := func(month, day, hour int) time.Time {
		return time.Date(2026, time.Month(month), day, hour, 0, 0, 0, time.UTC)
	}
	on := func(month, day int) time.Time { return at(month, day, 0) }
	record := func(id string, serviceTypeID uuid.UUID, occurred, created time.Time) model.Record {
		return model.Record{
			ID: uuid.MustParse(id), VehicleID: vehicleID, ServiceTypeID: serviceTypeID, CreatedBy: userID,
			OccurredOn: occurred, CreatedAt: created, UpdatedAt: created,
		}
	}
	oilBaseline := record("00000000-0000-0000-0000-000000000010", oilID, on(6, 1), at(6, 1, 12))
	oilOlder := record("00000000-0000-0000-0000-000000000011", oilID, on(1, 15), at(1, 15, 12))
	tireLater := record("00000000-0000-0000-0000-000000000012", tireID, on(9, 1), at(9, 1, 12))
	tireOlder := record("00000000-0000-0000-0000-000000000013", tireID, on(3, 1), at(3, 1, 12))
	for _, row := range []model.Record{oilBaseline, oilOlder, tireLater, tireOlder} {
		if _, err := store.CreateRecord(ctx, row, nil); err != nil {
			t.Fatal(err)
		}
	}

	until := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	miles := int64(15500)
	snoozeAt := at(7, 31, 15)
	for _, id := range []uuid.UUID{oilReminderID, tireReminderID} {
		if err := store.SetReminderSnooze(ctx, vehicleID, id, &until, &miles, snoozeAt); err != nil {
			t.Fatal(err)
		}
	}
	requireSnooze(t, "initial oil", store, ctx, vehicleID, oilID, "2026-11-13", &miles, &oilBaseline.ID)
	requireSnooze(t, "initial tires", store, ctx, vehicleID, tireID, "2026-11-13", &miles, &tireLater.ID)

	// Notes and vendor on the current baseline do not undo a later snooze.
	oilBaseline.Vendor = "Quick Lube"
	oilBaseline.Notes = "synthetic"
	oilBaseline.UpdatedAt = snoozeAt.Add(time.Hour)
	if _, err := store.UpdateRecord(ctx, oilBaseline); err != nil {
		t.Fatal(err)
	}
	stored, _, err := store.GetRecord(ctx, oilBaseline.ID)
	if err != nil || stored.Vendor != "Quick Lube" || stored.Notes != "synthetic" {
		t.Fatalf("notes edit stored=%+v err=%v", stored, err)
	}
	requireSnooze(t, "notes edit", store, ctx, vehicleID, oilID, "2026-11-13", &miles, &oilBaseline.ID)

	// Moving the current baseline's date later keeps it the baseline.
	oilBaseline.OccurredOn = on(6, 20)
	oilBaseline.UpdatedAt = oilBaseline.UpdatedAt.Add(time.Hour)
	if _, err = store.UpdateRecord(ctx, oilBaseline); err != nil {
		t.Fatal(err)
	}
	requireSnooze(t, "baseline date moved later", store, ctx, vehicleID, oilID, "2026-11-13", &miles, &oilBaseline.ID)

	// An older oil record dated ahead of that baseline becomes the match.
	oilOlder.OccurredOn = on(8, 1)
	oilOlder.UpdatedAt = oilBaseline.UpdatedAt.Add(time.Hour)
	if _, err = store.UpdateRecord(ctx, oilOlder); err != nil {
		t.Fatal(err)
	}
	requireSnooze(t, "date moved ahead", store, ctx, vehicleID, oilID, "", nil, &oilOlder.ID)
	requireSnooze(t, "tires after oil date edit", store, ctx, vehicleID, tireID, "2026-11-13", &miles, &tireLater.ID)

	if err = store.SetReminderSnooze(ctx, vehicleID, oilReminderID, &until, &miles, oilOlder.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	// A date edit that stays behind the baseline leaves the snooze set.
	oilBaseline.OccurredOn = on(6, 25)
	oilBaseline.UpdatedAt = oilOlder.UpdatedAt.Add(time.Hour)
	if _, err = store.UpdateRecord(ctx, oilBaseline); err != nil {
		t.Fatal(err)
	}
	requireSnooze(t, "date still behind", store, ctx, vehicleID, oilID, "2026-11-13", &miles, &oilOlder.ID)

	oilOlder.Notes = "still the baseline"
	oilOlder.UpdatedAt = oilBaseline.UpdatedAt.Add(time.Hour)
	if _, err = store.UpdateRecord(ctx, oilOlder); err != nil {
		t.Fatal(err)
	}
	requireSnooze(t, "baseline notes again", store, ctx, vehicleID, oilID, "2026-11-13", &miles, &oilOlder.ID)

	// Reclassifying a later tire record as oil makes it the oil baseline.
	tireLater.ServiceTypeID = oilID
	tireLater.UpdatedAt = oilOlder.UpdatedAt.Add(time.Hour)
	if _, err = store.UpdateRecord(ctx, tireLater); err != nil {
		t.Fatal(err)
	}
	requireSnooze(t, "reclassified onto oil", store, ctx, vehicleID, oilID, "", nil, &tireLater.ID)
	requireSnooze(t, "tires after reclassify", store, ctx, vehicleID, tireID, "2026-11-13", &miles, &tireOlder.ID)

	if err = store.SetReminderSnooze(ctx, vehicleID, oilReminderID, &until, &miles, tireLater.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	// Reclassifying an older tire record does not become the oil baseline.
	tireOlder.ServiceTypeID = oilID
	tireOlder.UpdatedAt = tireLater.UpdatedAt.Add(time.Hour)
	if _, err = store.UpdateRecord(ctx, tireOlder); err != nil {
		t.Fatal(err)
	}
	requireSnooze(t, "reclassified behind oil", store, ctx, vehicleID, oilID, "2026-11-13", &miles, &tireLater.ID)
	requireSnooze(t, "tires left with no records", store, ctx, vehicleID, tireID, "2026-11-13", &miles, nil)
}

func openSnoozeDB(t *testing.T) (context.Context, *repository.Postgres, *pgx.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	scopedURL := testdb.URL(t)
	connection, err := pgx.Connect(ctx, scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	store, err := repository.NewPostgres(ctx, scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return ctx, store, connection
}

func requireSnooze(t *testing.T, step string, store *repository.Postgres, ctx context.Context, vehicleID, serviceTypeID uuid.UUID, wantOn string, wantMiles *int64, wantBaseline *uuid.UUID) {
	t.Helper()
	rows, err := store.ListReminders(ctx, &vehicleID, true)
	if err != nil {
		t.Fatal(err)
	}
	var row *model.Reminder
	for i := range rows {
		if rows[i].ServiceTypeID == serviceTypeID {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("%s: reminder %s missing", step, serviceTypeID)
	}
	gotOn := ""
	if row.SnoozedUntilOn != nil {
		gotOn = row.SnoozedUntilOn.Format(time.DateOnly)
	}
	if gotOn != wantOn || !sameInt64(row.SnoozedUntilMiles, wantMiles) {
		t.Fatalf("%s: snooze until %q miles %v, want until %q miles %v", step, gotOn, int64Value(row.SnoozedUntilMiles), wantOn, int64Value(wantMiles))
	}
	switch {
	case wantBaseline == nil && row.Baseline != nil:
		t.Fatalf("%s: baseline %s, want none", step, row.Baseline.ID)
	case wantBaseline != nil && (row.Baseline == nil || row.Baseline.ID != *wantBaseline):
		got := "none"
		if row.Baseline != nil {
			got = row.Baseline.ID.String()
		}
		t.Fatalf("%s: baseline %s, want %s", step, got, *wantBaseline)
	}
}

func sameInt64(got, want *int64) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

func int64Value(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func months(v int) *int { return &v }
