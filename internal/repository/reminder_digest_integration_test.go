package repository_test

import (
	"context"
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

	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// Late Monday evening in New York is already Tuesday in UTC; the stored week
	// must still be the local Monday.
	monday := time.Date(2026, time.March, 16, 0, 0, 0, 0, newYork)
	if sent, err := store.ReminderDigestSent(ctx, monday); err != nil || sent {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	digest := model.ReminderDigest{ID: uuid.New(), WeekStart: monday, SentAt: time.Now(), Recipients: []string{"user@example.com"}, MessageID: "<digest-1@example.com>"}
	if err := store.CreateReminderDigest(ctx, digest); err != nil {
		t.Fatal(err)
	}
	if sent, err := store.ReminderDigestSent(ctx, monday); err != nil || !sent {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	if sent, err := store.ReminderDigestSent(ctx, monday.AddDate(0, 0, 7)); err != nil || sent {
		t.Fatalf("next week sent=%v err=%v", sent, err)
	}
	duplicate := digest
	duplicate.ID, duplicate.MessageID = uuid.New(), "<digest-2@example.com>"
	if err := store.CreateReminderDigest(ctx, duplicate); err == nil {
		t.Fatal("a second digest for the same week was recorded")
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
