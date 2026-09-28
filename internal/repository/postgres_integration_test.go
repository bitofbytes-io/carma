package repository_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/database"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// newPostgresIntegrationStore migrates an isolated schema and returns a store
// scoped to it. It skips unless CARMA_TEST_DATABASE_URL is set.
func newPostgresIntegrationStore(t *testing.T) (context.Context, *repository.Postgres) {
	t.Helper()
	baseURL := os.Getenv("CARMA_TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("set CARMA_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	schema := "carma_repo_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) })
	scopedURL, err := withSearchPath(baseURL, schema)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := pgx.Connect(ctx, scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close(context.Background()) }()
	if err = database.Migrate(ctx, connection, migrations.FS); err != nil {
		t.Fatal(err)
	}
	store, err := repository.NewPostgres(ctx, scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return ctx, store
}

func int64Pointer(value int64) *int64 { return &value }

func TestRecordsPostgresIntegration(t *testing.T) {
	ctx, postgres := newPostgresIntegrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := model.User{ID: uuid.New(), OAuthProvider: "google", OAuthSubject: "records", Email: "driver@example.com", DisplayName: "Driver", CreatedAt: now, UpdatedAt: now, LastLoginAt: now}
	car, truck := model.Vehicle{ID: uuid.New(), Nickname: "Car", CreatedAt: now, UpdatedAt: now}, model.Vehicle{ID: uuid.New(), Nickname: "Truck", CreatedAt: now, UpdatedAt: now}
	oil, tires := model.ServiceType{ID: uuid.New(), Name: "Test oil", CreatedAt: now}, model.ServiceType{ID: uuid.New(), Name: "Test tires", CreatedAt: now}
	date := func(month, day int) time.Time { return time.Date(2026, time.Month(month), day, 0, 0, 0, 0, time.UTC) }
	record := func(id string, vehicle model.Vehicle, serviceType model.ServiceType, occurred time.Time, odometer, cost *int64, vendor, notes string) model.Record {
		return model.Record{ID: uuid.MustParse(id), VehicleID: vehicle.ID, ServiceTypeID: serviceType.ID, CreatedBy: user.ID, OccurredOn: occurred, OdometerMiles: odometer, CostCents: cost, Vendor: vendor, Notes: notes, CreatedAt: now, UpdatedAt: now}
	}
	percent := record("00000000-0000-0000-0000-000000000001", car, oil, date(1, 10), int64Pointer(1000), int64Pointer(5000), "100% Synthetic", "")
	underscore := record("00000000-0000-0000-0000-000000000002", car, tires, date(3, 5), int64Pointer(3000), nil, "Tire_Shop", "rotated")
	plain := record("00000000-0000-0000-0000-000000000003", car, oil, date(5, 1), nil, int64Pointer(2000), "Tire Shop", "")
	other := record("00000000-0000-0000-0000-000000000004", truck, oil, date(2, 1), int64Pointer(500), int64Pointer(100), "Other", "Tire pressure checked")
	receipts := []model.Attachment{
		{ID: uuid.New(), RecordID: percent.ID, OriginalFilename: "a.pdf", ContentType: "application/pdf", ByteSize: 10, StorageKey: "aa/" + uuid.NewString() + ".pdf", CreatedAt: now},
		{ID: uuid.New(), RecordID: percent.ID, OriginalFilename: "b.jpg", ContentType: "image/jpeg", ByteSize: 20, StorageKey: "bb/" + uuid.NewString() + ".jpg", CreatedAt: now.Add(time.Second)},
	}

	// Seed both stores identically so every query also checks memory parity.
	memory := repository.NewMemory()
	for _, store := range []repository.Store{postgres, memory} {
		if _, err := store.UpsertUser(ctx, user); err != nil {
			t.Fatal(err)
		}
		for _, vehicle := range []model.Vehicle{car, truck} {
			if _, err := store.CreateVehicle(ctx, vehicle); err != nil {
				t.Fatal(err)
			}
		}
		for _, serviceType := range []model.ServiceType{oil, tires} {
			if _, err := store.CreateServiceType(ctx, serviceType); err != nil {
				t.Fatal(err)
			}
		}
		for _, r := range []model.Record{percent, underscore, plain, other} {
			var attachments []model.Attachment
			if r.ID == percent.ID {
				attachments = receipts
			}
			if _, err := store.CreateRecord(ctx, r, attachments); err != nil {
				t.Fatal(err)
			}
		}
	}

	from, to := date(2, 1), date(4, 30)
	for _, test := range []struct {
		name  string
		query model.RecordQuery
		want  []model.Record
	}{
		{"vehicle default newest first", model.RecordQuery{VehicleID: &car.ID, Desc: true}, []model.Record{plain, underscore, percent}},
		{"all vehicles", model.RecordQuery{Desc: true}, []model.Record{plain, underscore, other, percent}},
		{"service type", model.RecordQuery{ServiceTypeID: &oil.ID, Desc: true}, []model.Record{plain, other, percent}},
		{"inclusive date range", model.RecordQuery{From: &from, To: &to, Desc: true}, []model.Record{underscore, other}},
		{"percent is literal", model.RecordQuery{Search: "%", Desc: true}, []model.Record{percent}},
		{"underscore is literal", model.RecordQuery{Search: "Tire_", Desc: true}, []model.Record{underscore}},
		{"case-insensitive vendor and notes", model.RecordQuery{Search: "TIRE", Desc: true}, []model.Record{plain, underscore, other}},
		{"service type name", model.RecordQuery{VehicleID: &car.ID, Search: "test TIRES", Desc: true}, []model.Record{underscore}},
		{"date ascending", model.RecordQuery{VehicleID: &car.ID, Sort: "date"}, []model.Record{percent, underscore, plain}},
		{"mileage descending nulls last", model.RecordQuery{VehicleID: &car.ID, Sort: "mileage", Desc: true}, []model.Record{underscore, percent, plain}},
		{"mileage ascending nulls last", model.RecordQuery{VehicleID: &car.ID, Sort: "mileage"}, []model.Record{percent, underscore, plain}},
		{"cost descending nulls last", model.RecordQuery{VehicleID: &car.ID, Sort: "cost", Desc: true}, []model.Record{percent, plain, underscore}},
		{"cost ascending nulls last", model.RecordQuery{VehicleID: &car.ID, Sort: "cost"}, []model.Record{plain, percent, underscore}},
		{"unknown sort falls back to newest", model.RecordQuery{VehicleID: &car.ID, Sort: "vendor;DROP TABLE records"}, []model.Record{plain, underscore, percent}},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := make([]uuid.UUID, 0, len(test.want))
			for _, r := range test.want {
				want = append(want, r.ID)
			}
			for name, store := range map[string]repository.Store{"postgres": postgres, "memory": memory} {
				rows, err := store.ListRecords(ctx, test.query)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				got := make([]uuid.UUID, 0, len(rows))
				for _, row := range rows {
					got = append(got, row.ID)
				}
				if !slices.Equal(got, want) {
					t.Fatalf("%s: got %v, want %v", name, got, want)
				}
			}
		})
	}

	t.Run("create record with attachments", func(t *testing.T) {
		rows, err := postgres.ListRecords(ctx, model.RecordQuery{Search: "100%"})
		if err != nil || len(rows) != 1 || rows[0].AttachmentCount != 2 || rows[0].VehicleName != "Car" || rows[0].ServiceTypeName != "Test oil" || rows[0].CreatedByName != "Driver" {
			t.Fatalf("listed record=%+v err=%v", rows, err)
		}
		stored, attachments, err := postgres.GetRecord(ctx, percent.ID)
		if err != nil || !stored.OccurredOn.Equal(percent.OccurredOn) || *stored.CostCents != 5000 || len(attachments) != 2 {
			t.Fatalf("record=%+v attachments=%+v err=%v", stored, attachments, err)
		}
		for i, attachment := range attachments {
			if attachment.ID != receipts[i].ID || attachment.StorageKey != receipts[i].StorageKey || attachment.RecordID != percent.ID {
				t.Fatalf("attachment %d = %+v, want %+v", i, attachment, receipts[i])
			}
		}
		parent, attachment, err := postgres.GetAttachment(ctx, receipts[1].ID)
		if err != nil || parent.ID != percent.ID || parent.AttachmentCount != 2 || attachment.OriginalFilename != "b.jpg" {
			t.Fatalf("attachment lookup parent=%+v attachment=%+v err=%v", parent, attachment, err)
		}
	})

	t.Run("failed attachment insert rolls back record", func(t *testing.T) {
		failed := record("00000000-0000-0000-0000-000000000005", car, oil, date(6, 1), nil, nil, "Rolled back", "")
		duplicate := receipts[0]
		duplicate.ID, duplicate.RecordID = uuid.New(), failed.ID
		if _, err := postgres.CreateRecord(ctx, failed, []model.Attachment{duplicate}); err == nil {
			t.Fatal("duplicate storage key was accepted")
		}
		if _, _, err := postgres.GetRecord(ctx, failed.ID); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("record survived failed attachment insert: %v", err)
		}
	})

	t.Run("add and delete attachments", func(t *testing.T) {
		added := model.Attachment{ID: uuid.New(), OriginalFilename: "c.png", ContentType: "image/png", ByteSize: 30, StorageKey: "cc/" + uuid.NewString() + ".png", CreatedAt: now}
		if err := postgres.AddAttachments(ctx, underscore.ID, []model.Attachment{added}); err != nil {
			t.Fatal(err)
		}
		parent, stored, err := postgres.GetAttachment(ctx, added.ID)
		if err != nil || parent.ID != underscore.ID || stored.RecordID != underscore.ID {
			t.Fatalf("added attachment parent=%+v attachment=%+v err=%v", parent, stored, err)
		}
		key, err := postgres.DeleteAttachment(ctx, added.ID)
		if err != nil || key != added.StorageKey {
			t.Fatalf("deleted key=%q err=%v", key, err)
		}
		if _, err = postgres.DeleteAttachment(ctx, added.ID); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("second delete error=%v", err)
		}
	})

	t.Run("delete record returns attachment keys", func(t *testing.T) {
		keys, err := postgres.DeleteRecord(ctx, percent.ID)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(keys)
		want := []string{receipts[0].StorageKey, receipts[1].StorageKey}
		slices.Sort(want)
		if !slices.Equal(keys, want) {
			t.Fatalf("keys=%v want=%v", keys, want)
		}
		if _, _, err = postgres.GetAttachment(ctx, receipts[0].ID); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("attachment survived record delete: %v", err)
		}
		if keys, err = postgres.DeleteRecord(ctx, percent.ID); !errors.Is(err, repository.ErrNotFound) || keys != nil {
			t.Fatalf("missing record keys=%v err=%v", keys, err)
		}
		if keys, err = postgres.DeleteRecord(ctx, plain.ID); err != nil || len(keys) != 0 {
			t.Fatalf("record without receipts keys=%v err=%v", keys, err)
		}
	})
}

func TestAuthPostgresIntegration(t *testing.T) {
	ctx, postgres := newPostgresIntegrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := func(subject, email, name string) model.User {
		return model.User{ID: uuid.New(), OAuthProvider: "google", OAuthSubject: subject, Email: email, DisplayName: name, CreatedAt: now, UpdatedAt: now, LastLoginAt: now}
	}

	first, err := postgres.UpsertUser(ctx, user("subject-a", "a@example.com", "A"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("same identity merges profile changes", func(t *testing.T) {
		later := now.Add(time.Hour)
		changed := user("subject-a", "renamed@example.com", "Renamed")
		changed.CreatedAt, changed.UpdatedAt, changed.LastLoginAt = later, later, later
		merged, err := postgres.UpsertUser(ctx, changed)
		if err != nil || merged.ID != first.ID || !merged.CreatedAt.Equal(first.CreatedAt) || merged.Email != "renamed@example.com" || merged.DisplayName != "Renamed" || !merged.LastLoginAt.Equal(later) {
			t.Fatalf("merged=%+v err=%v", merged, err)
		}
		first = merged
	})

	t.Run("new identity with existing email merges case-insensitively", func(t *testing.T) {
		relinked, err := postgres.UpsertUser(ctx, user("subject-a2", "RENAMED@example.com", "Relinked"))
		if err != nil || relinked.ID != first.ID || relinked.OAuthSubject != "subject-a2" {
			t.Fatalf("relinked=%+v err=%v", relinked, err)
		}
		if found, err := postgres.FindUserByOAuth(ctx, "google", "subject-a"); err != nil || found != nil {
			t.Fatalf("old identity still resolves: %+v err=%v", found, err)
		}
		first = relinked
	})

	t.Run("identity and email owned by different users conflict", func(t *testing.T) {
		second, err := postgres.UpsertUser(ctx, user("subject-b", "b@example.com", "B"))
		if err != nil {
			t.Fatal(err)
		}
		for name, store := range map[string]repository.Store{"postgres": postgres, "memory": memoryWithUsers(t, first, second)} {
			if _, err := store.UpsertUser(ctx, user(first.OAuthSubject, "B@example.com", "Takeover")); !errors.Is(err, repository.ErrConflict) {
				t.Fatalf("%s: conflict error=%v, want ErrConflict", name, err)
			}
		}
		for _, want := range []model.User{first, second} {
			found, err := postgres.FindUserByOAuth(ctx, "google", want.OAuthSubject)
			if err != nil || found == nil || found.ID != want.ID || found.Email != want.Email {
				t.Fatalf("conflict changed user: found=%+v want=%+v err=%v", found, want, err)
			}
		}
	})

	t.Run("find session", func(t *testing.T) {
		session := model.Session{ID: uuid.New(), UserID: first.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour), UserAgent: "test", IPAddress: "127.0.0.1"}
		if err := postgres.CreateSession(ctx, session, "active-hash"); err != nil {
			t.Fatal(err)
		}
		found, owner, err := postgres.FindSession(ctx, "active-hash")
		if err != nil || found == nil || owner == nil || found.ID != session.ID || !found.ExpiresAt.Equal(session.ExpiresAt) || found.UserAgent != "test" || owner.ID != first.ID || owner.Email != first.Email {
			t.Fatalf("session=%+v user=%+v err=%v", found, owner, err)
		}
		if found, owner, err = postgres.FindSession(ctx, "unknown-hash"); err != nil || found != nil || owner != nil {
			t.Fatalf("unknown session=%+v user=%+v err=%v", found, owner, err)
		}
		if err = postgres.DeleteSession(ctx, session.ID); err != nil {
			t.Fatal(err)
		}
		if found, _, err = postgres.FindSession(ctx, "active-hash"); err != nil || found != nil {
			t.Fatalf("deleted session=%+v err=%v", found, err)
		}
	})

	t.Run("delete expired sessions", func(t *testing.T) {
		for hash, expires := range map[string]time.Time{"past": now.Add(-time.Second), "exact": now, "future": now.Add(time.Second)} {
			if err := postgres.CreateSession(ctx, model.Session{ID: uuid.New(), UserID: first.ID, CreatedAt: now, ExpiresAt: expires}, hash); err != nil {
				t.Fatal(err)
			}
		}
		deleted, err := postgres.DeleteExpiredSessions(ctx, now)
		if err != nil || deleted != 2 {
			t.Fatalf("deleted=%d err=%v", deleted, err)
		}
		if found, _, err := postgres.FindSession(ctx, "future"); err != nil || found == nil {
			t.Fatalf("unexpired session removed: %v", err)
		}
	})
}

func memoryWithUsers(t *testing.T, users ...model.User) *repository.Memory {
	t.Helper()
	memory := repository.NewMemory()
	for _, user := range users {
		if _, err := memory.UpsertUser(t.Context(), user); err != nil {
			t.Fatal(err)
		}
	}
	return memory
}
