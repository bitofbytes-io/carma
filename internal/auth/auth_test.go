package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/repository"
)

func newOIDCTestServer(t *testing.T, tokenHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize",
			"token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/token", tokenHandler)
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"keys":[]}`)) })
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestGoogleOIDCUsesDiscoveredEndpoints(t *testing.T) {
	server := newOIDCTestServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	g, err := newGoogleOIDC(t.Context(), server.URL, &http.Client{Timeout: time.Second}, "client", "secret", "https://carma.example/callback", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := g.AuthURL("state"); !strings.HasPrefix(got, server.URL+"/authorize?") {
		t.Fatalf("authorization URL = %q", got)
	}
}

func TestGoogleOIDCDiscoveryTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	start := time.Now()
	_, err := newGoogleOIDC(t.Context(), server.URL, &http.Client{Timeout: 40 * time.Millisecond}, "client", "secret", "callback", nil, nil)
	if err == nil {
		t.Fatal("discovery unexpectedly succeeded")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("discovery timeout took %v", elapsed)
	}
}

func TestGoogleOIDCTokenExchangeTimeout(t *testing.T) {
	release := make(chan struct{})
	server := newOIDCTestServer(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	g, err := newGoogleOIDC(t.Context(), server.URL, &http.Client{Timeout: 40 * time.Millisecond}, "client", "secret", "callback", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = g.Exchange(t.Context(), "code")
	if err == nil || !strings.Contains(err.Error(), "token exchange") {
		t.Fatalf("exchange error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("token timeout took %v", elapsed)
	}
}

func TestGoogleOIDCExchangePreservesEarlierCallerDeadline(t *testing.T) {
	release := make(chan struct{})
	server := newOIDCTestServer(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	g, err := newGoogleOIDC(t.Context(), server.URL, &http.Client{Timeout: time.Second}, "client", "secret", "callback", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = g.Exchange(ctx, "code")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exchange error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("caller deadline took %v", elapsed)
	}
}

func TestSessionsAreOpaqueAndStoredHashed(t *testing.T) {
	m := repository.NewMemory()
	s := NewService(m, 90*24*time.Hour)
	u, token, e := s.DevLogin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if len(token) < 40 || strings.Contains(token, u.Email) {
		t.Fatalf("bad token %q", token)
	}
	if found, e := s.Validate(t.Context(), token); e != nil || found == nil || found.ID != u.ID {
		t.Fatalf("found=%v err=%v", found, e)
	}
	if sess, _, _ := m.FindSession(t.Context(), token); sess != nil {
		t.Fatal("raw token was stored")
	}
}

func TestEmailAndDomainAllowlistIsCaseInsensitive(t *testing.T) {
	g := &GoogleOIDC{emails: map[string]struct{}{"person@example.com": {}}, domains: map[string]struct{}{"family.test": {}}}
	for _, email := range []string{"Person@Example.com", "anyone@FAMILY.TEST"} {
		if !g.Allowed(email) {
			t.Fatalf("expected %s allowed", email)
		}
	}
	if g.Allowed("stranger@elsewhere.test") {
		t.Fatal("non-allowlisted email accepted")
	}
}
