package config

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	StoreMemory     = "memory"
	StorePostgres   = "postgres"
	AuthDevelopment = "development"
	AuthGoogle      = "google"
)

type Config struct {
	AppEnv, Port, DataStore, DatabaseURL, AuthMode        string
	GoogleClientID, GoogleClientSecret, GoogleRedirectURL string
	AllowedEmails, AllowedDomains                         []string
	SessionTTL                                            time.Duration
	AssetRoot                                             string
	MaxUploadBytes                                        int64
	MaxMultipartBytes                                     int64
	ReminderEmail                                         ReminderEmail
}

type ReminderEmail struct {
	Enabled      bool
	SMTPHost     string
	SMTPUsername string
	SMTPPassword string
	FromAddress  string
	FromName     string
	TLSMode      string
	PublicURL    string
}

func Load() (*Config, error) {
	c := &Config{AppEnv: env("APP_ENV", "development"), Port: env("PORT", "4700"), DataStore: env("DATA_STORE", StorePostgres), AuthMode: env("AUTH_MODE", AuthGoogle), GoogleRedirectURL: env("AUTH_GOOGLE_REDIRECT_URL", "http://localhost:4700/api/auth/google/callback"), AssetRoot: env("ASSET_ROOT", ".local/carma-assets")}
	var err error
	if c.DatabaseURL, err = envOrFile("DATABASE_URL", "/run/secrets/carma_database_url"); err != nil {
		return nil, err
	}
	if c.GoogleClientID, err = envOrFile("AUTH_GOOGLE_CLIENT_ID", "/run/secrets/carma_google_client_id"); err != nil {
		return nil, err
	}
	if c.GoogleClientSecret, err = envOrFile("AUTH_GOOGLE_CLIENT_SECRET", "/run/secrets/carma_google_client_secret"); err != nil {
		return nil, err
	}
	c.AllowedEmails = csv(os.Getenv("AUTH_GOOGLE_ALLOWED_EMAILS"))
	c.AllowedDomains = csv(os.Getenv("AUTH_GOOGLE_ALLOWED_DOMAINS"))
	if c.SessionTTL, err = time.ParseDuration(env("SESSION_TTL", "2160h")); err != nil || c.SessionTTL <= 0 {
		return nil, fmt.Errorf("SESSION_TTL must be a positive Go duration")
	}
	if c.MaxUploadBytes, err = strconv.ParseInt(env("MAX_UPLOAD_BYTES", "26214400"), 10, 64); err != nil || c.MaxUploadBytes <= 0 {
		return nil, fmt.Errorf("MAX_UPLOAD_BYTES must be positive")
	}
	if c.MaxMultipartBytes, err = strconv.ParseInt(env("MAX_MULTIPART_BYTES", "134217728"), 10, 64); err != nil || c.MaxMultipartBytes <= c.MaxUploadBytes {
		return nil, fmt.Errorf("MAX_MULTIPART_BYTES must be greater than MAX_UPLOAD_BYTES")
	}
	if c.ReminderEmail.Enabled, err = strconv.ParseBool(env("REMINDER_EMAIL_ENABLED", "false")); err != nil {
		return nil, fmt.Errorf("REMINDER_EMAIL_ENABLED must be true or false")
	}
	if c.ReminderEmail.Enabled {
		c.ReminderEmail.SMTPHost = strings.TrimSpace(os.Getenv("SMTP_HOST"))
		c.ReminderEmail.SMTPUsername = strings.TrimSpace(os.Getenv("SMTP_USERNAME"))
		if os.Getenv("SMTP_PASSWORD") != "" {
			return nil, fmt.Errorf("SMTP_PASSWORD is not supported; use SMTP_PASSWORD_FILE")
		}
		if c.ReminderEmail.SMTPPassword, err = requiredSecretFile("SMTP_PASSWORD_FILE"); err != nil {
			return nil, err
		}
		c.ReminderEmail.FromAddress = strings.TrimSpace(os.Getenv("SMTP_FROM_ADDRESS"))
		c.ReminderEmail.FromName = strings.TrimSpace(os.Getenv("SMTP_FROM_NAME"))
		c.ReminderEmail.TLSMode = strings.ToLower(strings.TrimSpace(os.Getenv("SMTP_TLS_MODE")))
		c.ReminderEmail.PublicURL = strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_URL")), "/")
		if err = validateReminderEmail(c.ReminderEmail); err != nil {
			return nil, err
		}
	}
	if c.DataStore != StoreMemory && c.DataStore != StorePostgres {
		return nil, fmt.Errorf("DATA_STORE must be memory or postgres")
	}
	if c.ReminderEmail.Enabled && c.DataStore != StorePostgres {
		return nil, fmt.Errorf("reminder email requires DATA_STORE=postgres")
	}
	if c.AuthMode != AuthDevelopment && c.AuthMode != AuthGoogle {
		return nil, fmt.Errorf("AUTH_MODE must be development or google")
	}
	if strings.EqualFold(c.AppEnv, "production") && c.AuthMode == AuthDevelopment {
		return nil, fmt.Errorf("development auth is forbidden in production")
	}
	if c.DataStore == StorePostgres && c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required for postgres")
	}
	if c.AuthMode == AuthGoogle && (c.GoogleClientID == "" || c.GoogleClientSecret == "" || (len(c.AllowedEmails) == 0 && len(c.AllowedDomains) == 0)) {
		return nil, fmt.Errorf("Google credentials and an email/domain allowlist are required")
	}
	return c, nil
}

func requiredSecretFile(key string) (string, error) {
	path := strings.TrimSpace(os.Getenv(key))
	if path == "" {
		return "", fmt.Errorf("%s is required when reminder email is enabled", key)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", key, err)
	}
	value := strings.TrimSpace(string(contents))
	if value == "" {
		return "", fmt.Errorf("%s secret is empty", key)
	}
	return value, nil
}

func validateReminderEmail(c ReminderEmail) error {
	for key, value := range map[string]string{
		"SMTP_HOST": c.SMTPHost, "SMTP_USERNAME": c.SMTPUsername, "SMTP_PASSWORD_FILE": c.SMTPPassword,
		"SMTP_FROM_ADDRESS": c.FromAddress, "SMTP_FROM_NAME": c.FromName, "SMTP_TLS_MODE": c.TLSMode, "PUBLIC_URL": c.PublicURL,
	} {
		if value == "" {
			return fmt.Errorf("%s is required when reminder email is enabled", key)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s contains an invalid newline", key)
		}
	}
	host, port, err := net.SplitHostPort(c.SMTPHost)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("SMTP_HOST must be host:port")
	}
	if c.TLSMode != "implicit" {
		return fmt.Errorf("SMTP_TLS_MODE must be implicit")
	}
	address, err := mail.ParseAddress(c.FromAddress)
	if err != nil || address.Address != c.FromAddress {
		return fmt.Errorf("SMTP_FROM_ADDRESS must be a plain valid email address")
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("PUBLIC_URL must be an HTTPS origin")
	}
	return nil
}

func (c *Config) SecureCookies() bool { return strings.EqualFold(c.AppEnv, "production") }
func LoadDatabaseURL() (string, error) {
	return envOrFile("DATABASE_URL", "/run/secrets/carma_database_url")
}
func env(k, d string) string {
	if f := os.Getenv(k + "_FILE"); f != "" {
		if b, e := os.ReadFile(f); e == nil {
			return strings.TrimSpace(string(b))
		}
	}
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func envOrFile(k, d string) (string, error) {
	if v := os.Getenv(k); v != "" {
		return v, nil
	}
	p := os.Getenv(k + "_FILE")
	if p == "" {
		p = d
	}
	if p == "" {
		return "", nil
	}
	b, e := os.ReadFile(p)
	if errors.Is(e, os.ErrNotExist) {
		return "", nil
	}
	if e != nil {
		return "", fmt.Errorf("read %s: %w", k, e)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s secret is empty", k)
	}
	return v, nil
}
func csv(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			out = append(out, v)
		}
	}
	return out
}
