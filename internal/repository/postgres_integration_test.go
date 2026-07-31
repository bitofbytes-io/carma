package repository_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/database"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestPostgresProductionPaths(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	schema := "carma_it_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		c, e := pgx.Connect(cleanup, dsn)
		if e == nil {
			_, _ = c.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE")
			_ = c.Close(cleanup)
		}
	})
	scoped := withSearchPath(t, dsn, schema)
	conn, err := pgx.Connect(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(ctx, conn, migrations.FS); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}
	_ = conn.Close(ctx)
	store, err := repository.NewPostgres(ctx, withPoolMaxConns(t, scoped, "1"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	authService := auth.NewService(store, 90*24*time.Hour)
	user, token, err := authService.Login(ctx, auth.Claims{Subject: "postgres-user", Email: "User@Example.com", Name: "Postgres User", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := authService.Validate(ctx, token)
	if err != nil || validated == nil || validated.ID != user.ID {
		t.Fatalf("session persistence failed: user=%v err=%v", validated, err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	vehicle, err := store.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: "PG Outback", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	types, err := store.ListServiceTypes(ctx)
	if err != nil || len(types) < 2 {
		t.Fatalf("seeded types: %d %v", len(types), err)
	}
	typ := types[0]
	date := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	miles := int64(1000)
	r1 := model.Record{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), VehicleID: vehicle.ID, ServiceTypeID: typ.ID, CreatedBy: user.ID, OccurredOn: date, Vendor: "Alpha", CreatedAt: now, UpdatedAt: now}
	r2 := r1
	r2.ID = uuid.MustParse("00000000-0000-0000-0000-000000000002")
	r2.Vendor = "Beta"
	r2.OdometerMiles = &miles
	r2.CreatedAt = now.Add(time.Second)
	r2.UpdatedAt = r2.CreatedAt
	if _, err = store.CreateRecord(ctx, r1, nil); err != nil {
		t.Fatal(err)
	}
	a := model.Attachment{ID: uuid.New(), RecordID: r2.ID, OriginalFilename: "receipt.pdf", ContentType: "application/pdf", ByteSize: 12, StorageKey: "ab/00000000-0000-0000-0000-000000000001.pdf", CreatedAt: now}
	if _, err = store.CreateRecord(ctx, r2, []model.Attachment{a}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateVehicle(ctx, model.Vehicle{ID: uuid.New(), Nickname: "PG Tacoma", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	listCtx, stopList := context.WithTimeout(ctx, 2*time.Second)
	defer stopList()
	vehicles, err := store.ListVehicles(listCtx, false)
	if err != nil || len(vehicles) != 2 || vehicles[0].LastRecord == nil || vehicles[0].LastRecord.ID != r2.ID {
		t.Fatalf("single-connection vehicle list: %+v %v", vehicles, err)
	}
	rows, err := store.ListRecords(ctx, model.RecordQuery{VehicleID: &vehicle.ID, Search: "beta", Sort: "mileage"})
	if err != nil || len(rows) != 1 || rows[0].ID != r2.ID || rows[0].AttachmentCount != 1 {
		t.Fatalf("filtered records: %+v %v", rows, err)
	}
	all, err := store.ListRecords(ctx, model.RecordQuery{VehicleID: &vehicle.ID, Sort: "mileage"})
	if err != nil || len(all) != 2 || all[1].ID != r1.ID {
		t.Fatalf("NULLS LAST order: %+v %v", all, err)
	}
	_, attachments, err := store.GetRecord(ctx, r2.ID)
	if err != nil || len(attachments) != 1 {
		t.Fatalf("attachment metadata: %+v %v", attachments, err)
	}
	attachmentRecord, attachmentByID, err := store.GetAttachment(ctx, a.ID)
	if err != nil || attachmentRecord.ID != r2.ID || attachmentByID.ID != a.ID {
		t.Fatalf("attachment lookup: record=%+v attachment=%+v err=%v", attachmentRecord, attachmentByID, err)
	}
	if _, _, err = store.GetAttachment(ctx, uuid.New()); err != repository.ErrNotFound {
		t.Fatalf("attachment not found: %v", err)
	}
	a2 := model.Attachment{ID: uuid.New(), RecordID: r2.ID, OriginalFilename: "photo.png", ContentType: "image/png", ByteSize: 8, StorageKey: "cd/00000000-0000-0000-0000-000000000002.png", CreatedAt: now.Add(time.Second)}
	if err = store.AddAttachments(ctx, r2.ID, []model.Attachment{a2}); err != nil {
		t.Fatal(err)
	}
	deletedKey, err := store.DeleteAttachment(ctx, a2.ID)
	if err != nil || deletedKey != a2.StorageKey {
		t.Fatalf("attachment delete key=%q err=%v", deletedKey, err)
	}

	months := 6
	reminderRow, err := store.UpsertReminder(ctx, model.Reminder{ID: uuid.New(), VehicleID: vehicle.ID, ServiceTypeID: typ.ID, IntervalMonths: &months, Enabled: true, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	reminders, err := store.ListReminders(ctx, &vehicle.ID, true)
	if err != nil || len(reminders) != 1 || reminders[0].Baseline == nil || reminders[0].Baseline.ID != r2.ID {
		t.Fatalf("reminder baseline: %+v %v", reminders, err)
	}
	if err = store.DeleteReminder(ctx, reminderRow.ID); err != nil {
		t.Fatal(err)
	}
	reminders, _ = store.ListReminders(ctx, &vehicle.ID, true)
	if len(reminders) != 0 {
		t.Fatal("reminder delete did not persist")
	}
	keys, err := store.DeleteRecord(ctx, r2.ID)
	if err != nil || len(keys) != 1 || keys[0] != a.StorageKey {
		t.Fatalf("record delete keys=%v err=%v", keys, err)
	}
}

func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func withPoolMaxConns(t *testing.T, dsn, max string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("pool_max_conns", max)
	u.RawQuery = q.Encode()
	return u.String()
}
