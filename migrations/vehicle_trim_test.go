package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestVehicleTrimMigrationIsAdditive(t *testing.T) {
	up, err := fs.ReadFile(FS, "012_vehicle_trim.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(up))
	for _, declaration := range []string{
		"alter table vehicles add column trim text not null default ''",
		"alter table vehicle_recall_checks",
		"add column trim text not null default ''",
		"add column match_version integer not null default 0",
	} {
		if !strings.Contains(sql, declaration) {
			t.Fatalf("vehicle trim migration missing %q", declaration)
		}
	}
	for _, statement := range []string{"drop ", "delete from", "update "} {
		if strings.Contains(sql, statement) {
			t.Fatalf("vehicle trim migration must only add columns, found %q", statement)
		}
	}
	down, err := fs.ReadFile(FS, "012_vehicle_trim.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := strings.ToLower(string(down))
	for _, declaration := range []string{
		"delete from schema_migrations where version = '012_vehicle_trim'",
		"alter table vehicle_recall_checks drop column if exists match_version",
		"alter table vehicle_recall_checks drop column if exists trim",
		"alter table vehicles drop column if exists trim",
	} {
		if !strings.Contains(downSQL, declaration) {
			t.Fatalf("vehicle trim down migration missing %q", declaration)
		}
	}
}
