package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
)

type sessionErrorStore struct {
	repository.Store
	err error
}

func (s sessionErrorStore) FindSession(context.Context, string) (*model.Session, *model.User, error) {
	return nil, nil, s.err
}

func TestRequireAuthReturnsUnavailableWithoutClearingCookieOnStoreError(t *testing.T) {
	storeErr := errors.New("session store unavailable")
	service := auth.NewService(sessionErrorStore{Store: repository.NewMemory(), err: storeErr}, time.Hour)
	called := false
	handler := RequireAuth(service, true)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	request := httptest.NewRequest(http.MethodGet, "https://carma.example/vehicles", nil)
	request.AddCookie(&http.Cookie{Name: CookieName, Value: "valid-shape-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("status=%d next-called=%v", response.Code, called)
	}
	if cookies := response.Header().Values("Set-Cookie"); len(cookies) != 0 {
		t.Fatalf("store failure changed session cookie: %v", cookies)
	}
}

func TestRequireAuthClearsInvalidSessionAndRedirects(t *testing.T) {
	service := auth.NewService(repository.NewMemory(), time.Hour)
	handler := RequireAuth(service, true)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("invalid session reached protected handler")
	}))
	request := httptest.NewRequest(http.MethodGet, "https://carma.example/vehicles/123?sort=cost", nil)
	request.AddCookie(&http.Cookie{Name: CookieName, Value: "invalid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || !strings.HasPrefix(response.Header().Get("Location"), "/login?redirect=") {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != CookieName || cookies[0].MaxAge >= 0 || !cookies[0].Secure {
		t.Fatalf("invalid session cookie was not cleared securely: %+v", cookies)
	}
}
