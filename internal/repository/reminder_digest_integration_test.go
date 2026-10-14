package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/database"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/internal/testdb"
	"github.com/bitofbytes-io/carma/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestReminderDigestPostgresIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	scopedURL := testdb.URL(t)
	store, err := repository.NewPostgres(ctx, scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var _ repository.ReminderDigestStore = store

	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	// Monday midnight in Tokyo is still Sunday in UTC; the stored week must
	// still be the local Monday.
	monday := time.Date(2026, time.March, 16, 0, 0, 0, 0, tokyo)
	if claimed, err := store.ReminderDigestClaimed(ctx, monday); err != nil || claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	digest := model.ReminderDigest{ID: uuid.New(), WeekStart: monday, ReservedAt: time.Now(), Recipients: []string{"user@example.com"}}
	if reserved, err := store.ReserveReminderDigest(ctx, digest); err != nil || !reserved {
		t.Fatalf("reserved=%v err=%v", reserved, err)
	}
	if claimed, err := store.ReminderDigestClaimed(ctx, monday); err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	if claimed, err := store.ReminderDigestClaimed(ctx, monday.AddDate(0, 0, 7)); err != nil || claimed {
		t.Fatalf("next week claimed=%v err=%v", claimed, err)
	}
	duplicate := digest
	duplicate.ID = uuid.New()
	if reserved, err := store.ReserveReminderDigest(ctx, duplicate); err != nil || reserved {
		t.Fatalf("second reservation for the same week: reserved=%v err=%v", reserved, err)
	}

	// Releasing an unsent claim lets the week be retried.
	if err := store.ReleaseReminderDigest(ctx, digest.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ReminderDigestClaimed(ctx, monday); err != nil || claimed {
		t.Fatalf("claimed after release=%v err=%v", claimed, err)
	}
	if reserved, err := store.ReserveReminderDigest(ctx, duplicate); err != nil || !reserved {
		t.Fatalf("reserved after release=%v err=%v", reserved, err)
	}
	if err := store.CompleteReminderDigest(ctx, duplicate.ID, "<digest-1@example.com>", time.Now()); err != nil {
		t.Fatal(err)
	}
	// A completed digest is never released.
	if err := store.ReleaseReminderDigest(ctx, duplicate.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ReminderDigestClaimed(ctx, monday); err != nil || !claimed {
		t.Fatalf("claimed after completed release=%v err=%v", claimed, err)
	}
	if err := store.CompleteReminderDigest(ctx, uuid.New(), "<digest-2@example.com>", time.Now()); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("complete unknown digest err=%v", err)
	}

	unlock, acquired, err := store.TryReminderDigestLock(ctx)
	if err != nil || !acquired {
		t.Fatalf("acquired=%v err=%v", acquired, err)
	}
	if _, again, err := store.TryReminderDigestLock(ctx); err != nil || again {
		t.Fatalf("second lock acquired=%v err=%v", again, err)
	}
	if err := unlock(ctx); err != nil {
		t.Fatal(err)
	}

	connection, err := pgx.Connect(ctx, scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close(context.Background()) }()
	var stored string
	if err = connection.QueryRow(ctx, `SELECT week_start::text FROM reminder_digests WHERE id=$1`, duplicate.ID).Scan(&stored); err != nil || stored != "2026-03-16" {
		t.Fatalf("stored week_start=%q err=%v", stored, err)
	}
	down, err := migrations.FS.ReadFile("005_reminder_digests.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(ctx, connection, migrations.FS); err != nil {
		t.Fatalf("reapply after down: %v", err)
	}
	var rows int
	if err = connection.QueryRow(ctx, `SELECT count(*) FROM reminder_digests`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows after down/up=%d err=%v", rows, err)
	}
}
