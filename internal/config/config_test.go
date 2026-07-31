package config

import "testing"

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
