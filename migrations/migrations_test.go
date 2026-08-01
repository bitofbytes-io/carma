package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestInitialMigrationDownPreservesFrameworkAndAllowsReapply(t *testing.T) {
	down, err := fs.ReadFile(FS, "001_initial.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(down))
	if !strings.Contains(sql, "delete from schema_migrations where version = '001_initial'") {
		t.Fatal("down migration does not clear the applied version")
	}
	if strings.Contains(sql, "drop table if exists schema_migrations") || strings.Contains(sql, "drop table schema_migrations") {
		t.Fatal("down migration drops the migration framework table")
	}
	if !strings.Contains(sql, "drop table if exists") {
		t.Fatal("down migration does not remove application tables")
	}
}

func TestInitialMigrationDeclaresForeignKeyIndexes(t *testing.T) {
	up, err := fs.ReadFile(FS, "001_initial.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(up))
	for _, declaration := range []string{
		"create index attachments_record_id_idx on attachments(record_id)",
		"create index records_created_by_idx on records(created_by)",
		"create index records_service_type_id_idx on records(service_type_id)",
	} {
		if !strings.Contains(sql, declaration) {
			t.Fatalf("initial migration missing %q", declaration)
		}
	}
}

func TestReminderBaselineMigrationColumnsConstraintsAndBackfill(t *testing.T) {
	up, err := fs.ReadFile(FS, "002_reminder_baselines.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(up))
	for _, declaration := range []string{
		"add column current_odometer_miles bigint",
		"vehicles_current_odometer_nonnegative",
		"current_odometer_miles is null or current_odometer_miles >= 0",
		"select max(r.odometer_miles)",
		"add column starting_odometer_miles bigint",
		"reminders_starting_odometer_nonnegative",
		"starting_odometer_miles is null or starting_odometer_miles >= 0",
		"add column starting_odometer_pending boolean not null default false",
		"reminders_starting_odometer_pending_state",
		"not starting_odometer_pending or starting_odometer_miles is null",
		"set starting_odometer_miles = v.current_odometer_miles",
		"starting_odometer_pending = (v.current_odometer_miles is null)",
	} {
		if !strings.Contains(sql, declaration) {
			t.Fatalf("reminder baseline migration missing %q", declaration)
		}
	}
	down, err := fs.ReadFile(FS, "002_reminder_baselines.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := strings.ToLower(string(down))
	for _, declaration := range []string{
		"delete from schema_migrations where version = '002_reminder_baselines'",
		"drop column if exists starting_odometer_pending",
		"drop column if exists starting_odometer_miles",
		"drop column if exists current_odometer_miles",
	} {
		if !strings.Contains(downSQL, declaration) {
			t.Fatalf("reminder baseline down migration missing %q", declaration)
		}
	}
}

func TestReminderNotificationsMigrationAuditShape(t *testing.T) {
	up, err := fs.ReadFile(FS, "003_reminder_notifications.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(up))
	for _, declaration := range []string{
		"create table reminder_notifications",
		"references reminders(id) on delete cascade",
		"sent_at timestamptz not null",
		"recipients text[] not null",
		"message_id text not null unique",
		"reminder_notifications_reminder_sent_idx",
		"(reminder_id, sent_at desc)",
	} {
		if !strings.Contains(sql, declaration) {
			t.Fatalf("notification migration missing %q", declaration)
		}
	}
	down, err := fs.ReadFile(FS, "003_reminder_notifications.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := strings.ToLower(string(down))
	for _, declaration := range []string{
		"delete from schema_migrations where version = '003_reminder_notifications'",
		"drop table if exists reminder_notifications",
	} {
		if !strings.Contains(downSQL, declaration) {
			t.Fatalf("notification down migration missing %q", declaration)
		}
	}
}
