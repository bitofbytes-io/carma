package repository_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/database"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
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
	defer func() { _ = admin.Close(context.Background()) }()

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
	downMigration, err := migrations.FS.ReadFile("003_reminder_notifications.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = migrationConnection.Exec(context.Background(), string(downMigration)); err != nil {
		t.Fatal(err)
	}
	var tableName *string
	if err = migrationConnection.QueryRow(context.Background(), `SELECT to_regclass('reminder_notifications')::text`).Scan(&tableName); err != nil {
		t.Fatal(err)
	}
	var versionPresent bool
	if err = migrationConnection.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='003_reminder_notifications')`).Scan(&versionPresent); err != nil {
		t.Fatal(err)
	}
	if tableName != nil || versionPresent {
		t.Fatalf("down migration left table=%v version=%t", tableName, versionPresent)
	}
	if err = database.Migrate(context.Background(), migrationConnection, migrations.FS); err != nil {
		t.Fatalf("reapply after down migration: %v", err)
	}
	if err = migrationConnection.QueryRow(context.Background(), `SELECT to_regclass('reminder_notifications')::text`).Scan(&tableName); err != nil {
		t.Fatal(err)
	}
	if tableName == nil {
		t.Fatal("reapply did not recreate reminder_notifications")
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
	defer func() { _ = seed.Close(context.Background()) }()
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

	cycleVehicleID, cycleServiceID, cycleReminderID := uuid.New(), uuid.New(), uuid.New()
	cycleBaselineCreated := time.Date(2026, time.March, 22, 12, 0, 0, 0, time.UTC)
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO vehicles(id,nickname) VALUES($1,'New cycle vehicle')`, []any{cycleVehicleID}},
		{`INSERT INTO service_types(id,name) VALUES($1,'New cycle service')`, []any{cycleServiceID}},
		{`INSERT INTO reminders(id,vehicle_id,service_type_id,interval_months) VALUES($1,$2,$3,1)`, []any{cycleReminderID, cycleVehicleID, cycleServiceID}},
		{`INSERT INTO records(id,vehicle_id,service_type_id,occurred_on,created_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$6)`, []any{uuid.New(), cycleVehicleID, cycleServiceID, time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC), userID, cycleBaselineCreated}},
	} {
		if _, err = seed.Exec(context.Background(), statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	cycleReminders, err := storeOne.ListReminders(context.Background(), &cycleVehicleID, false)
	if err != nil || len(cycleReminders) != 1 || cycleReminders[0].Baseline == nil {
		t.Fatalf("cycle reminders=%+v err=%v", cycleReminders, err)
	}
	cycleRunTime := time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC)
	if result := reminder.Evaluate(cycleReminders[0], cycleRunTime); result.Status != reminder.Due {
		t.Fatalf("new cycle status = %s, want overdue", result.Status)
	}
	priorCycleNotification := model.ReminderNotification{ID: uuid.New(), ReminderID: cycleReminderID, SentAt: cycleBaselineCreated.Add(-time.Second), Recipients: []string{"user@example.com"}, MessageID: "<prior-cycle@bitofbytes.io>"}
	if err = storeOne.CreateReminderNotification(context.Background(), priorCycleNotification); err != nil {
		t.Fatal(err)
	}
	if suppressed, err := storeOne.ReminderNotificationSince(context.Background(), cycleReminderID, cycleReminders[0].Baseline.CreatedAt); err != nil || suppressed {
		t.Fatalf("prior-cycle notification suppressed=%t err=%v", suppressed, err)
	}
	currentCycleNotification := model.ReminderNotification{ID: uuid.New(), ReminderID: cycleReminderID, SentAt: cycleBaselineCreated.Add(time.Second), Recipients: []string{"user@example.com"}, MessageID: "<current-cycle@bitofbytes.io>"}
	if err = storeOne.CreateReminderNotification(context.Background(), currentCycleNotification); err != nil {
		t.Fatal(err)
	}
	if suppressed, err := storeOne.ReminderNotificationSince(context.Background(), cycleReminderID, cycleReminders[0].Baseline.CreatedAt); err != nil || !suppressed {
		t.Fatalf("current-cycle notification suppressed=%t err=%v", suppressed, err)
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

	limitedURL, err := withDatabaseParameter(scopedURL, "pool_max_conns", "1")
	if err != nil {
		t.Fatal(err)
	}
	limitedStore, err := repository.NewPostgres(context.Background(), limitedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer limitedStore.Close()
	unlockLimited, acquired, err := limitedStore.TryReminderEmailLock(context.Background())
	if err != nil || !acquired {
		t.Fatalf("limited-pool lock acquired=%t err=%v", acquired, err)
	}
	shortContext, cancelShort := context.WithTimeout(context.Background(), 75*time.Millisecond)
	started := time.Now()
	_, _, err = limitedStore.TryReminderEmailLock(shortContext)
	cancelShort()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("limited-pool acquire error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("caller acquire deadline took %v", elapsed)
	}
	if err = unlockLimited(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func withSearchPath(databaseURL, schema string) (string, error) {
	return withDatabaseParameter(databaseURL, "search_path", schema)
}

func withDatabaseParameter(databaseURL, key, value string) (string, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parse test database URL: %w", err)
	}
	query := parsed.Query()
	query.Set(key, value)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
