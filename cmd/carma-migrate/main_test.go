package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/testdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestMain(m *testing.M) {
	if os.Getenv("CARMA_MIGRATE_CHILD") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRunRejectsMissingDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DATABASE_URL_FILE", filepath.Join(t.TempDir(), "missing"))
	var buf bytes.Buffer
	err := run(context.Background(), newLogger(&buf))
	if err == nil || err.Error() != "DATABASE_URL is required" {
		t.Fatalf("error = %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("logged before configuration failed: %s", buf.String())
	}
}

func TestRunRejectsInvalidTimezone(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://carma:carma@127.0.0.1:1/carma?sslmode=disable")
	t.Setenv("DATABASE_URL_FILE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("APP_TIMEZONE", "Not/AZone")
	err := run(context.Background(), newLogger(&bytes.Buffer{}))
	if err == nil || !strings.Contains(err.Error(), "APP_TIMEZONE") {
		t.Fatalf("error = %v", err)
	}
}

func TestMigrateMissingDatabaseURLExit(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = envWithoutDatabaseURL(t)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("exit error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if code := exitErr.ExitCode(); code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr:\n%s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	record := requireJSONMessage(t, stderr.String(), "carma migrate stopped")
	if record["level"] != "ERROR" || record["error"] != "DATABASE_URL is required" {
		t.Fatalf("record = %#v", record)
	}
}

func TestRunMigratesPostgresAndLogsVersion(t *testing.T) {
	databaseURL := freshSchemaURL(t)
	t.Setenv("DATABASE_URL", databaseURL)
	t.Setenv("DATABASE_URL_FILE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("APP_TIMEZONE", "UTC")

	var buf bytes.Buffer
	if err := run(context.Background(), newLogger(&buf)); err != nil {
		t.Fatal(err)
	}
	records := decodeLogRecords(t, buf.String())
	if len(records) != 2 {
		t.Fatalf("records = %#v", records)
	}
	starting, applied := records[0], records[1]
	version, _ := applied["version"].(string)
	if starting["msg"] != "migration starting" || starting["level"] != "INFO" || starting["version"] == "" {
		t.Fatalf("start record = %#v", starting)
	}
	if applied["msg"] != "migrations applied" || applied["level"] != "INFO" || version == "" {
		t.Fatalf("finish record = %#v", applied)
	}
	if starting["version"] != version {
		t.Fatalf("start version %v, applied version %s", starting["version"], version)
	}
	assertSchemaVersion(t, databaseURL, version)

	buf.Reset()
	if err := run(context.Background(), newLogger(&buf)); err != nil {
		t.Fatal(err)
	}
	again := decodeLogRecords(t, buf.String())
	if len(again) != 2 || again[1]["version"] != version || again[1]["msg"] != "migrations applied" {
		t.Fatalf("second run records = %#v", again)
	}
}

func TestCmdPackagesDoNotImportStandardLog(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test file path unavailable")
	}
	cmdDir := filepath.Dir(filepath.Dir(file))
	entries, err := os.ReadDir(cmdDir)
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(cmdDir, entry.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".go") {
				continue
			}
			name := filepath.Join(dir, file.Name())
			parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			checked++
			for _, imp := range parsed.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if path == "log" {
					t.Errorf("%s imports \"log\"", name)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("parsed no Go files under cmd")
	}
}

func envWithoutDatabaseURL(t *testing.T) []string {
	t.Helper()
	filtered := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "DATABASE_URL", "DATABASE_URL_FILE", "CARMA_MIGRATE_CHILD":
			continue
		}
		filtered = append(filtered, entry)
	}
	missing := filepath.Join(t.TempDir(), "missing-database-url")
	return append(filtered, "CARMA_MIGRATE_CHILD=1", "DATABASE_URL_FILE="+missing)
}

func requireJSONMessage(t *testing.T, output, message string) map[string]any {
	t.Helper()
	records := decodeLogRecords(t, output)
	for _, record := range records {
		if record["msg"] == message {
			return record
		}
	}
	t.Fatalf("no JSON record with msg %q in:\n%s", message, output)
	return nil
}

func decodeLogRecords(t *testing.T, output string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		t.Fatalf("no JSON log records in:\n%s", output)
	}
	return records
}

func assertSchemaVersion(t *testing.T, databaseURL, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var got string
	if err := conn.QueryRow(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("schema version = %s, want %s", got, want)
	}
}

func freshSchemaURL(t *testing.T) string {
	t.Helper()
	baseURL := os.Getenv(testdb.EnvVar)
	if baseURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s must be set in CI", testdb.EnvVar)
		}
		t.Skipf("set %s to run PostgreSQL-backed tests", testdb.EnvVar)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	schema := "carma_migrate_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, `DROP SCHEMA `+quoted+` CASCADE`); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})
	scoped, err := testdb.WithParameter(baseURL, "search_path", schema)
	if err != nil {
		t.Fatal(err)
	}
	return scoped
}
