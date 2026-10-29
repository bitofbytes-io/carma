package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/assets"
	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/config"
	"github.com/bitofbytes-io/carma/internal/middleware"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/testdb"
	"github.com/google/uuid"
)

type fakeGoogle struct {
	claims  auth.Claims
	allowed bool
}

func (f fakeGoogle) AuthURL(state string) string {
	return "https://accounts.example/auth?state=" + state
}
func (f fakeGoogle) Exchange(_ context.Context, _ string) (auth.Claims, error) { return f.claims, nil }
func (f fakeGoogle) Allowed(_ string) bool                                     { return f.allowed }

func TestLogoutRetainsCookieWhenSessionRevocationFails(t *testing.T) {
	f := setup(t)
	revocationErr := errors.New("session revocation failed")
	f.s.auth = auth.NewService(deleteSessionErrorStore{Store: f.store, err: revocationErr}, f.s.cfg.SessionTTL, nil)
	f.router = f.s.Router()

	response := f.do(t, http.MethodPost, "/logout", nil, "")

	if response.Code != http.StatusInternalServerError || response.Header().Get("Location") != "" {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	if cookies := response.Header().Values("Set-Cookie"); len(cookies) != 0 {
		t.Fatalf("revocation failure changed browser cookie: %v", cookies)
	}
	if !strings.Contains(response.Body.String(), "Something went wrong") {
		t.Fatalf("revocation failure presented as success: %q", response.Body.String())
	}
	user, err := f.s.auth.Validate(t.Context(), f.cookie.Value)
	if err != nil || user == nil {
		t.Fatalf("session was not reusable after failed revocation: user=%v err=%v", user, err)
	}
}

func TestLogoutRevokesSessionAndClearsCookie(t *testing.T) {
	f := setup(t)

	response := f.do(t, http.MethodPost, "/logout", nil, "")

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != middleware.CookieName || cookies[0].Value != "" || cookies[0].MaxAge >= 0 {
		t.Fatalf("logout did not clear browser cookie: %+v", cookies)
	}
	user, err := f.s.auth.Validate(t.Context(), f.cookie.Value)
	if err != nil || user != nil {
		t.Fatalf("session remains valid after logout: user=%v err=%v", user, err)
	}
}

func TestGoogleCallbackAcceptsAllowlistedAndFriendlyRejectsOthers(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		allowed, verified, wantSession bool
	}{
		{"allowlisted", true, true, true}, {"not allowlisted", false, true, false}, {"unverified", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := testdb.Store(t)
			a, _ := assets.NewLocalStore(t.TempDir())
			cfg := &config.Config{AppEnv: "production", AuthMode: config.AuthGoogle, SessionTTL: 90 * 24 * time.Hour, MaxUploadBytes: 25 << 20}
			svc := auth.NewService(store, cfg.SessionTTL, nil)
			g := fakeGoogle{claims: auth.Claims{Subject: "sub", Email: "person@example.com", Name: "Person", EmailVerified: tc.verified}, allowed: tc.allowed}
			srv, e := New(cfg, store, a, svc, g)
			if e != nil {
				t.Fatal(e)
			}
			router := srv.Router()
			start := httptest.NewRecorder()
			router.ServeHTTP(start, httptest.NewRequest("GET", "https://carma.example/api/auth/google", nil))
			var state *http.Cookie
			for _, c := range start.Result().Cookies() {
				if c.Name == oauthStateCookie {
					state = c
				}
			}
			if state == nil {
				t.Fatal("missing state cookie")
			}
			cb := httptest.NewRequest("GET", "https://carma.example/api/auth/google/callback?state="+url.QueryEscape(state.Value)+"&code=ok", nil)
			cb.AddCookie(state)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, cb)
			hasSession := false
			for _, c := range w.Result().Cookies() {
				if c.Name == middleware.CookieName && c.Value != "" {
					hasSession = true
					if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
						t.Fatalf("insecure cookie: %+v", c)
					}
				}
			}
			if hasSession != tc.wantSession {
				t.Fatalf("session=%v location=%s", hasSession, w.Header().Get("Location"))
			}
			if !tc.wantSession && w.Header().Get("Location") != "/login?error="+loginErrorNotInvited {
				t.Fatalf("unfriendly rejection: %s", w.Header().Get("Location"))
			}
		})
	}
}

func TestGoogleCallbackReportsIdentityConflict(t *testing.T) {
	store := testdb.Store(t)
	now := time.Now()
	for _, existing := range []model.User{
		{ID: uuid.New(), OAuthProvider: "google", OAuthSubject: "sub-a", Email: "a@example.com", CreatedAt: now},
		{ID: uuid.New(), OAuthProvider: "google", OAuthSubject: "sub-b", Email: "b@example.com", CreatedAt: now},
	} {
		if _, err := store.UpsertUser(t.Context(), existing); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := assets.NewLocalStore(t.TempDir())
	cfg := &config.Config{AppEnv: "production", AuthMode: config.AuthGoogle, SessionTTL: time.Hour, MaxUploadBytes: 25 << 20}
	g := fakeGoogle{claims: auth.Claims{Subject: "sub-a", Email: "b@example.com", EmailVerified: true}, allowed: true}
	srv, err := New(cfg, store, a, auth.NewService(store, cfg.SessionTTL, nil), g)
	if err != nil {
		t.Fatal(err)
	}
	router := srv.Router()
	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest("GET", "https://carma.example/api/auth/google", nil))
	state := start.Result().Cookies()[0]
	callback := httptest.NewRequest("GET", "https://carma.example/api/auth/google/callback?state="+url.QueryEscape(state.Value)+"&code=ok", nil)
	callback.AddCookie(state)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, callback)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login?error="+loginErrorConflict {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == middleware.CookieName && cookie.Value != "" {
			t.Fatal("conflicting sign-in created a session")
		}
	}
}

func TestGoogleOAuthPreservesOnlySafeDeepLinks(t *testing.T) {
	store := testdb.Store(t)
	a, err := assets.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		AppEnv:             "production",
		AuthMode:           config.AuthGoogle,
		GoogleClientSecret: "state-signing-secret",
		SessionTTL:         90 * 24 * time.Hour,
		MaxUploadBytes:     25 << 20,
	}
	svc := auth.NewService(store, cfg.SessionTTL, nil)
	g := fakeGoogle{
		claims:  auth.Claims{Subject: "sub", Email: "person@example.com", Name: "Person", EmailVerified: true},
		allowed: true,
	}
	srv, err := New(cfg, store, a, svc, g)
	if err != nil {
		t.Fatal(err)
	}
	router := srv.Router()

	completeLogin := func(t *testing.T, requestedTarget string) *httptest.ResponseRecorder {
		t.Helper()
		start := httptest.NewRecorder()
		startRequest := httptest.NewRequest("GET", "https://carma.example/api/auth/google?redirect="+url.QueryEscape(requestedTarget), nil)
		router.ServeHTTP(start, startRequest)

		var state *http.Cookie
		for _, cookie := range start.Result().Cookies() {
			if cookie.Name == oauthStateCookie {
				state = cookie
				break
			}
		}
		if state == nil {
			t.Fatal("missing OAuth state cookie")
		}

		callback := httptest.NewRequest("GET", "https://carma.example/api/auth/google/callback?state="+url.QueryEscape(state.Value)+"&code=ok", nil)
		callback.AddCookie(state)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, callback)
		return response
	}

	deepLink := "/vehicles/123?sort=cost&direction=asc"
	login := httptest.NewRecorder()
	router.ServeHTTP(login, httptest.NewRequest("GET", "https://carma.example/login?redirect="+url.QueryEscape(deepLink), nil))
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), "/api/auth/google?redirect=%2Fvehicles%2F123%3Fsort%3Dcost%26direction%3Dasc") {
		t.Fatalf("login did not preserve deep link: %d %s", login.Code, login.Body.String())
	}

	if response := completeLogin(t, deepLink); response.Code != http.StatusSeeOther || response.Header().Get("Location") != deepLink {
		t.Fatalf("safe redirect: %d %q", response.Code, response.Header().Get("Location"))
	}

	for _, unsafe := range []string{
		"https://evil.example/steal",
		"//evil.example/steal",
		`/%2f%2fevil.example/steal`,
		`/\evil.example/steal`,
		`/%5c%5cevil.example/steal`,
		`/%zz`,
	} {
		t.Run("reject "+unsafe, func(t *testing.T) {
			response := completeLogin(t, unsafe)
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/" {
				t.Fatalf("unsafe redirect %q: %d %q", unsafe, response.Code, response.Header().Get("Location"))
			}
		})
	}
}

func TestGoogleOAuthRejectsTamperedState(t *testing.T) {
	store := testdb.Store(t)
	a, err := assets.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AppEnv: "production", AuthMode: config.AuthGoogle, GoogleClientSecret: "state-signing-secret", SessionTTL: 90 * 24 * time.Hour, MaxUploadBytes: 25 << 20}
	svc := auth.NewService(store, cfg.SessionTTL, nil)
	g := fakeGoogle{claims: auth.Claims{Subject: "sub", Email: "person@example.com", Name: "Person", EmailVerified: true}, allowed: true}
	srv, err := New(cfg, store, a, svc, g)
	if err != nil {
		t.Fatal(err)
	}
	router := srv.Router()

	state, err := signedOAuthState("nonce", "/vehicles/123", cfg.GoogleClientSecret)
	if err != nil {
		t.Fatal(err)
	}
	tampered := "A" + state[1:]
	if tampered == state {
		tampered = "B" + state[1:]
	}
	callback := httptest.NewRequest("GET", "https://carma.example/api/auth/google/callback?state="+url.QueryEscape(tampered)+"&code=ok", nil)
	callback.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: tampered})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, callback)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login?error="+loginErrorExpired {
		t.Fatalf("tampered state accepted: %d %q", response.Code, response.Header().Get("Location"))
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == middleware.CookieName && cookie.Value != "" {
			t.Fatal("tampered state created a session")
		}
	}
}

func TestLoginPageAllowsOnlyKnownErrorCodes(t *testing.T) {
	f := setup(t)
	for _, tc := range []struct {
		code, message string
	}{
		{loginErrorExpired, "Sign-in expired. Please try again."},
		{loginErrorOAuth, "Google sign-in could not be completed. Please try again."},
		{loginErrorNotInvited, "This verified Google account is not invited to Carma."},
		{loginErrorConflict, "This Google account&#39;s email already belongs to another Carma user."},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://example.com/login?error="+url.QueryEscape(tc.code), nil)
		f.router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), tc.message) {
			t.Fatalf("known code %q: status=%d body=%s", tc.code, response.Code, response.Body.String())
		}
	}

	attackerText := `<script>ATTACKER-CONTROLLED</script>`
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.com/login?error="+url.QueryEscape(attackerText), nil)
	f.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "ATTACKER-CONTROLLED") {
		t.Fatalf("unknown error rendered attacker text: status=%d body=%s", response.Code, response.Body.String())
	}
}
