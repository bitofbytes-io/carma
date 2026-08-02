package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevelopmentAuthForbiddenInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("AUTH_MODE", "development")
	t.Setenv("DATA_STORE", "memory")
	if _, e := Load(); e == nil {
		t.Fatal("development auth accepted in production")
	}
}
func TestGoogleRequiresAllowlist(t *testing.T) {
	for _, k := range []string{"APP_ENV", "AUTH_MODE", "DATA_STORE", "AUTH_GOOGLE_CLIENT_ID", "AUTH_GOOGLE_CLIENT_SECRET", "AUTH_GOOGLE_ALLOWED_EMAILS", "AUTH_GOOGLE_ALLOWED_DOMAINS"} {
		t.Setenv(k, "")
	}
	t.Setenv("DATA_STORE", "memory")
	t.Setenv("AUTH_MODE", "google")
	t.Setenv("AUTH_GOOGLE_CLIENT_ID", "id")
	t.Setenv("AUTH_GOOGLE_CLIENT_SECRET", "secret")
	if _, e := Load(); e == nil {
		t.Fatal("google auth accepted without allowlist")
	}
	t.Setenv("AUTH_GOOGLE_ALLOWED_DOMAINS", "example.com")
	if _, e := Load(); e != nil {
		t.Fatal(e)
	}
}

func TestReminderEmailLoadsPasswordFileAndValidates(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "smtp-password")
	if err := os.WriteFile(passwordFile, []byte(" secret-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setReminderEmailEnvironment(t)
	t.Setenv("SMTP_PASSWORD_FILE", passwordFile)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReminderEmail.SMTPPassword != "secret-password" || cfg.ReminderEmail.SMTPHost != "mail.bitofbytes.io:465" {
		t.Fatalf("SMTP config = %+v", cfg.ReminderEmail)
	}
}

func TestReminderEmailRejectsIncompleteInvalidAndMemoryConfig(t *testing.T) {
	setReminderEmailEnvironment(t)
	t.Setenv("SMTP_TLS_MODE", "starttls")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SMTP_TLS_MODE") {
		t.Fatalf("invalid TLS mode error = %v", err)
	}
	setReminderEmailEnvironment(t)
	t.Setenv("PUBLIC_URL", "http://carma.bitofbytes.io")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PUBLIC_URL") {
		t.Fatalf("invalid public URL error = %v", err)
	}
	setReminderEmailEnvironment(t)
	t.Setenv("DATA_STORE", "memory")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATA_STORE=postgres") {
		t.Fatalf("memory email error = %v", err)
	}
}

func TestReminderEmailPasswordFileIsMandatory(t *testing.T) {
	t.Run("missing path", func(t *testing.T) {
		setReminderEmailEnvironment(t)
		t.Setenv("SMTP_PASSWORD_FILE", "")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SMTP_PASSWORD_FILE is required") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("unreadable path", func(t *testing.T) {
		setReminderEmailEnvironment(t)
		t.Setenv("SMTP_PASSWORD_FILE", t.TempDir())
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "read SMTP_PASSWORD_FILE") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("empty file", func(t *testing.T) {
		setReminderEmailEnvironment(t)
		path := filepath.Join(t.TempDir(), "empty")
		if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SMTP_PASSWORD_FILE", path)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "secret is empty") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("environment bypass", func(t *testing.T) {
		setReminderEmailEnvironment(t)
		t.Setenv("SMTP_PASSWORD", "must-not-be-used")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("error = %v", err)
		}
	})
}

func setReminderEmailEnvironment(t *testing.T) {
	t.Helper()
	passwordFile := filepath.Join(t.TempDir(), "smtp-password")
	if err := os.WriteFile(passwordFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"APP_ENV": "development", "AUTH_MODE": "development", "DATA_STORE": "postgres", "DATABASE_URL": "postgres://example",
		"REMINDER_EMAIL_ENABLED": "true", "SMTP_HOST": "mail.bitofbytes.io:465", "SMTP_USERNAME": "carma",
		"SMTP_PASSWORD": "", "SMTP_PASSWORD_FILE": passwordFile, "SMTP_FROM_ADDRESS": "carma@bitofbytes.io", "SMTP_FROM_NAME": "Carma",
		"SMTP_TLS_MODE": "implicit", "PUBLIC_URL": "https://carma.bitofbytes.io",
	} {
		t.Setenv(key, value)
	}
}
