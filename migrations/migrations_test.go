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

func TestOdometerReadingsMigrationHistoryShape(t *testing.T) {
	up, err := fs.ReadFile(FS, "004_odometer_readings.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(up))
	for _, declaration := range []string{
		"create table odometer_readings",
		"references vehicles(id) on delete cascade",
		"read_on date not null",
		"odometer_miles bigint not null check (odometer_miles >= 0)",
		"created_by uuid not null references users(id)",
		"odometer_readings_vehicle_read_on_idx",
		"(vehicle_id, read_on desc)",
		"create index odometer_readings_created_by_idx on odometer_readings(created_by)",
	} {
		if !strings.Contains(sql, declaration) {
			t.Fatalf("odometer readings migration missing %q", declaration)
		}
	}
	down, err := fs.ReadFile(FS, "004_odometer_readings.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := strings.ToLower(string(down))
	for _, declaration := range []string{
		"delete from schema_migrations where version = '004_odometer_readings'",
		"drop table if exists odometer_readings",
	} {
		if !strings.Contains(downSQL, declaration) {
			t.Fatalf("odometer readings down migration missing %q", declaration)
		}
	}
}

func TestVehicleRecallsMigrationIsAdditive(t *testing.T) {
	up, err := fs.ReadFile(FS, "006_vehicle_recalls.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(up))
	for _, declaration := range []string{
		"create table vehicle_recalls",
		"create table vehicle_recall_checks",
		"primary key (vehicle_id, campaign_number)",
		"vehicle_id uuid primary key references vehicles(id) on delete cascade",
	} {
		if !strings.Contains(sql, declaration) {
			t.Fatalf("vehicle recalls migration missing %q", declaration)
		}
	}
	for _, statement := range []string{"alter table", "drop ", "delete from", "update "} {
		if strings.Contains(sql, statement) {
			t.Fatalf("vehicle recalls migration must only add tables, found %q", statement)
		}
	}
	down, err := fs.ReadFile(FS, "006_vehicle_recalls.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := strings.ToLower(string(down))
	for _, declaration := range []string{
		"delete from schema_migrations where version = '006_vehicle_recalls'",
		"drop table if exists vehicle_recall_checks",
		"drop table if exists vehicle_recalls",
	} {
		if !strings.Contains(downSQL, declaration) {
			t.Fatalf("vehicle recalls down migration missing %q", declaration)
		}
	}
}

func TestVehicleOdometerDateMigrationBackfill(t *testing.T) {
	up, err := fs.ReadFile(FS, "007_vehicle_odometer_date.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(up))
	for _, declaration := range []string{
		"alter table vehicles add column current_odometer_on date",
		"from odometer_readings o",
		"from records r",
		"(v.updated_at at time zone current_setting('carma.household_timezone'))::date",
		"where v.current_odometer_miles is not null",
	} {
		if !strings.Contains(sql, declaration) {
			t.Fatalf("vehicle odometer date migration missing %q", declaration)
		}
	}
	down, err := fs.ReadFile(FS, "007_vehicle_odometer_date.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := strings.ToLower(string(down))
	for _, declaration := range []string{
		"delete from schema_migrations where version = '007_vehicle_odometer_date'",
		"drop column if exists current_odometer_on",
	} {
		if !strings.Contains(downSQL, declaration) {
			t.Fatalf("vehicle odometer date down migration missing %q", declaration)
		}
	}
}

func TestRecallNotificationsMigrationIsAdditiveWithBackfill(t *testing.T) {
	up, err := fs.ReadFile(FS, "008_recall_notifications.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(up))
	for _, declaration := range []string{
		"alter table vehicle_recalls add column notified_at timestamptz",
		"alter table vehicle_recalls add column notified_digest_id uuid",
		"update vehicle_recalls set notified_at = first_seen_at",
	} {
		if !strings.Contains(sql, declaration) {
			t.Fatalf("recall notifications migration missing %q", declaration)
		}
	}
	for _, statement := range []string{"drop ", "delete from", "set not null", "timestamptz not null"} {
		if strings.Contains(sql, statement) {
			t.Fatalf("recall notifications migration must only add nullable columns, found %q", statement)
		}
	}
	down, err := fs.ReadFile(FS, "008_recall_notifications.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := strings.ToLower(string(down))
	for _, declaration := range []string{
		"delete from schema_migrations where version = '008_recall_notifications'",
		"drop column if exists notified_at",
		"drop column if exists notified_digest_id",
	} {
		if !strings.Contains(downSQL, declaration) {
			t.Fatalf("recall notifications down migration missing %q", declaration)
		}
	}
}
