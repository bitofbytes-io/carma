package repository_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
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

func TestReminderEmailPostgresIntegration(t *testing.T) {
	baseURL := os.Getenv("CARMA_TEST_DATABASE_URL")
	if baseURL == "" {
		baseURL = "postgres://carma:carma@localhost:5435/carma?sslmode=disable"
	}
	connectContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	admin, err := pgx.Connect(connectContext, baseURL)
	if err != nil {
		t.Skipf("integration Postgres unavailable (%v); run `make test-integration`", err)
	}
	defer admin.Close(context.Background())

	schema := "carma_email_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) }()
	scopedURL, err := withSearchPath(baseURL, schema)
	if err != nil {
		t.Fatal(err)
	}
	migrationConnection, err := pgx.Connect(context.Background(), scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(context.Background(), migrationConnection, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err = migrationConnection.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	storeOne, err := repository.NewPostgres(context.Background(), scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer storeOne.Close()
	storeTwo, err := repository.NewPostgres(context.Background(), scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer storeTwo.Close()

	seed, err := pgx.Connect(context.Background(), scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close(context.Background())
	userID, vehicleID, serviceTypeID, reminderID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users(id,oauth_provider,oauth_subject,email) VALUES($1,'test','subject','user@example.com')`, []any{userID}},
		{`INSERT INTO vehicles(id,nickname) VALUES($1,'Integration vehicle')`, []any{vehicleID}},
		{`INSERT INTO service_types(id,name) VALUES($1,'Integration service')`, []any{serviceTypeID}},
		{`INSERT INTO reminders(id,vehicle_id,service_type_id,interval_months) VALUES($1,$2,$3,1)`, []any{reminderID, vehicleID, serviceTypeID}},
	} {
		if _, err = seed.Exec(context.Background(), statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	sentAt := time.Date(2026, time.April, 1, 12, 0, 0, 123, time.UTC)
	notification := model.ReminderNotification{ID: uuid.New(), ReminderID: reminderID, SentAt: sentAt, Recipients: []string{"user@example.com"}, MessageID: "<integration@bitofbytes.io>"}
	if err = storeOne.CreateReminderNotification(context.Background(), notification); err != nil {
		t.Fatal(err)
	}
	if suppressed, err := storeOne.ReminderNotificationSince(context.Background(), reminderID, sentAt); err != nil || !suppressed {
		t.Fatalf("inclusive boundary suppressed=%t err=%v", suppressed, err)
	}
	// PostgreSQL timestamptz stores microsecond precision, so use a comfortably
	// representable cutoff after the stored send rather than a nanosecond delta.
	if suppressed, err := storeOne.ReminderNotificationSince(context.Background(), reminderID, sentAt.Add(time.Second)); err != nil || suppressed {
		t.Fatalf("after boundary suppressed=%t err=%v", suppressed, err)
	}

	unlockOne, acquired, err := storeOne.TryReminderEmailLock(context.Background())
	if err != nil || !acquired {
		t.Fatalf("first lock acquired=%t err=%v", acquired, err)
	}
	if _, acquired, err = storeTwo.TryReminderEmailLock(context.Background()); err != nil || acquired {
		t.Fatalf("contending lock acquired=%t err=%v", acquired, err)
	}
	if err = unlockOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	unlockTwo, acquired, err := storeTwo.TryReminderEmailLock(context.Background())
	if err != nil || !acquired {
		t.Fatalf("reacquired lock acquired=%t err=%v", acquired, err)
	}
	if err = unlockTwo(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func withSearchPath(databaseURL, schema string) (string, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parse test database URL: %w", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
