package repository_test

import (
	"context"
	"io/fs"
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
	connections := make([]*pgx.Conn, 2)
	for i := range connections {
		connections[i], err = pgx.Connect(ctx, scoped)
		if err != nil {
			t.Fatal(err)
		}
	}
	startMigrations := make(chan struct{})
	migrationResults := make(chan error, len(connections))
	for _, connection := range connections {
		go func(conn *pgx.Conn) {
			<-startMigrations
			migrationResults <- database.Migrate(ctx, conn, migrations.FS)
		}(connection)
	}
	close(startMigrations)
	for range connections {
		if err = <-migrationResults; err != nil {
			t.Fatalf("concurrent embedded migration: %v", err)
		}
	}
	migrationFiles, err := fs.Glob(migrations.FS, "*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	var appliedMigrations int
	if err = connections[0].QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&appliedMigrations); err != nil || appliedMigrations != len(migrationFiles) {
		t.Fatalf("applied migrations=%d want=%d err=%v", appliedMigrations, len(migrationFiles), err)
	}
	for _, connection := range connections {
		if err = connection.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}

	concurrentStore, err := repository.NewPostgres(ctx, withPoolMaxConns(t, scoped, "4"))
	if err != nil {
		t.Fatal(err)
	}
	concurrentAuth := auth.NewService(concurrentStore, 90*24*time.Hour)
	type loginResult struct {
		user model.User
		err  error
	}
	const loginRunners = 8
	startLogins := make(chan struct{})
	loginResults := make(chan loginResult, loginRunners)
	now := time.Now().UTC().Truncate(time.Microsecond)
	concurrentClaims := auth.Claims{Subject: "concurrent-user", Email: "concurrent@example.com", Name: "Concurrent User", EmailVerified: true}
	for i := 0; i < loginRunners; i++ {
		go func() {
			<-startLogins
			user, _, loginErr := concurrentAuth.Login(ctx, concurrentClaims)
			loginResults <- loginResult{user: user, err: loginErr}
		}()
	}
	close(startLogins)
	var persistedUserID uuid.UUID
	for i := 0; i < loginRunners; i++ {
		result := <-loginResults
		if result.err != nil {
			t.Fatalf("concurrent first login: %v", result.err)
		}
		if persistedUserID == uuid.Nil {
			persistedUserID = result.user.ID
		} else if result.user.ID != persistedUserID {
			t.Fatalf("logins returned different persisted IDs: %s and %s", persistedUserID, result.user.ID)
		}
	}
	byIdentity, err := concurrentStore.FindUserByOAuth(ctx, "google", concurrentClaims.Subject)
	if err != nil || byIdentity == nil || byIdentity.ID != persistedUserID {
		t.Fatalf("concurrent identity lookup: user=%+v err=%v", byIdentity, err)
	}
	byEmail, err := concurrentStore.FindUserByEmail(ctx, concurrentClaims.Email)
	if err != nil || byEmail == nil || byEmail.ID != persistedUserID {
		t.Fatalf("concurrent email lookup: user=%+v err=%v", byEmail, err)
	}

	legacy, _, err := concurrentAuth.Login(ctx, auth.Claims{Subject: "legacy-subject", Email: "linked@example.com", Name: "Legacy Name", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	linked, _, err := concurrentAuth.Login(ctx, auth.Claims{Subject: "linked-subject", Email: "LINKED@example.com", Name: "Linked Name", EmailVerified: true})
	if err != nil || linked.ID != legacy.ID || linked.CreatedAt != legacy.CreatedAt {
		t.Fatalf("email-linked login: legacy=%+v linked=%+v err=%v", legacy, linked, err)
	}
	oldIdentity, err := concurrentStore.FindUserByOAuth(ctx, "google", "legacy-subject")
	if err != nil || oldIdentity != nil {
		t.Fatalf("legacy identity still linked: user=%+v err=%v", oldIdentity, err)
	}
	linkedIdentity, err := concurrentStore.FindUserByOAuth(ctx, "google", "linked-subject")
	if err != nil || linkedIdentity == nil || linkedIdentity.ID != legacy.ID || linkedIdentity.DisplayName != "Linked Name" {
		t.Fatalf("new identity link: user=%+v err=%v", linkedIdentity, err)
	}
	concurrentStore.Close()

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
	r2.OccurredOn = date.AddDate(0, 0, 1)
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
	for _, query := range []model.RecordQuery{
		{VehicleID: &vehicle.ID, Sort: "unrecognized"},
		{VehicleID: &vehicle.ID, Sort: "unrecognized", Desc: true},
	} {
		all, err = store.ListRecords(ctx, query)
		if err != nil || len(all) != 2 || all[0].ID != r2.ID {
			t.Fatalf("unknown sort did not normalize to date descending: %+v %v", all, err)
		}
	}
	for _, query := range []struct {
		value model.RecordQuery
		first uuid.UUID
	}{
		{value: model.RecordQuery{VehicleID: &vehicle.ID, Sort: "date"}, first: r1.ID},
		{value: model.RecordQuery{VehicleID: &vehicle.ID, Sort: "date", Desc: true}, first: r2.ID},
	} {
		all, err = store.ListRecords(ctx, query.value)
		if err != nil || len(all) != 2 || all[0].ID != query.first {
			t.Fatalf("date sort first=%s rows=%+v err=%v", query.first, all, err)
		}
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
