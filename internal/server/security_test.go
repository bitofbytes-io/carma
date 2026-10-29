package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/config"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/google/uuid"
)

func assertSecurityHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	for name, want := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "same-origin",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'",
	} {
		if got := response.Result().Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

type panicSessionStore struct{ repository.Store }

func (panicSessionStore) DeleteSession(context.Context, uuid.UUID) error {
	panic("test recovered response")
}

func TestHealthIsPublic(t *testing.T) {
	f := setup(t)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("GET", "http://example.com/health", nil))
	if w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
}

func TestSecurityHeadersOnPublicResponses(t *testing.T) {
	t.Chdir("../..")
	srv, err := New(&config.Config{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := srv.Router()
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/health", http.StatusOK},
		{http.MethodGet, "/login", http.StatusOK},
		{http.MethodGet, "/", http.StatusSeeOther},
		{http.MethodGet, "/missing", http.StatusNotFound},
		{http.MethodPost, "/logout", http.StatusForbidden},
		{http.MethodPut, "/health", http.StatusMethodNotAllowed},
		{http.MethodGet, "/static/app.js", http.StatusOK},
		{http.MethodGet, "/static/styles.css", http.StatusOK},
		{http.MethodGet, "/static/missing", http.StatusNotFound},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "http://example.com"+tc.path, nil)
			if tc.method == http.MethodPut {
				request.Header.Set("Origin", "http://example.com")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d, want %d", response.Code, tc.status)
			}
			assertSecurityHeaders(t, response)
		})
	}
	request := httptest.NewRequest(http.MethodGet, "http://example.com/static/app.js", nil)
	request.Header.Set("If-None-Match", `"`+strings.Split(srv.static.URL("app.js"), "?v=")[1]+`"`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotModified {
		t.Fatalf("cached asset status=%d", response.Code)
	}
	assertSecurityHeaders(t, response)
}

func TestSecurityHeadersOnAuthenticatedResponses(t *testing.T) {
	f := setup(t)
	record, attachment := createAttachment(t, f)
	vehiclePath := "/vehicles/" + record.VehicleID.String()
	for _, tc := range []struct {
		name, method, path, origin string
		authenticated, htmx        bool
		status                     int
	}{
		{"dashboard", "GET", "/", "", true, false, 200},
		{"history", "GET", vehiclePath + "/history", "", true, false, 200},
		{"attachment", "GET", "/attachments/" + attachment.ID.String(), "", true, false, 200},
		{"attachment requires auth", "GET", "/attachments/" + attachment.ID.String(), "", false, false, 303},
		{"CSV download", "GET", "/records/export.csv", "", true, false, 200},
		{"XLSX download", "GET", "/records/export.xlsx", "", true, false, 200},
		{"HTMX partial", "GET", vehiclePath + "?q=oil", "", true, true, 200},
		{"HTMX requires auth", "GET", vehiclePath, "", false, true, 200},
		{"cross-origin rejection", "POST", "/logout", "https://other.example", true, false, 403},
		{"invalid vehicle", "GET", "/vehicles/invalid", "", true, false, 404},
		{"OAuth rejection", "GET", "/api/auth/google/callback", "", false, false, 303},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "http://example.com"+tc.path, nil)
			request.Header.Set("Origin", tc.origin)
			if tc.authenticated {
				request.AddCookie(f.cookie)
			}
			if tc.htmx {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			f.router.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d, want %d", response.Code, tc.status)
			}
			assertSecurityHeaders(t, response)
			if tc.htmx && !tc.authenticated && response.Header().Get("HX-Redirect") == "" {
				t.Fatal("missing HTMX authentication redirect")
			}
		})
	}
	for _, store := range []repository.Store{
		deleteSessionErrorStore{Store: f.store, err: errors.New("test error")},
		panicSessionStore{Store: f.store},
	} {
		f.s.auth = auth.NewService(store, f.s.cfg.SessionTTL, nil)
		f.router = f.s.Router()
		response := f.do(t, http.MethodPost, "/logout", nil, "")
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d", response.Code)
		}
		assertSecurityHeaders(t, response)
	}
}
