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
