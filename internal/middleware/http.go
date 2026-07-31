package middleware

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/model"
)

const CookieName = "carma_session"

type userKey struct{}

func User(r *http.Request) *model.User { u, _ := r.Context().Value(userKey{}).(*model.User); return u }
func RequireAuth(s *auth.Service, secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, e := r.Cookie(CookieName)
			if e != nil || c.Value == "" {
				redirect(w, r)
				return
			}
			u, e := s.Validate(r.Context(), c.Value)
			if e != nil || u == nil {
				ClearSession(w, secure)
				redirect(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
		})
	}
}
func SetSession(w http.ResponseWriter, token string, secure bool, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds())})
}
func ClearSession(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Path: "/", Value: "", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(0, 0)})
}
func redirect(w http.ResponseWriter, r *http.Request) {
	target := "/login"
	if r.URL.Path != "/" {
		target += "?redirect=" + url.QueryEscape(r.URL.RequestURI())
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
func SameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		want := origin(r)
		got := r.Header.Get("Origin")
		if got == "" {
			if ref := r.Header.Get("Referer"); ref != "" {
				u, e := url.Parse(ref)
				if e == nil {
					got = u.Scheme + "://" + u.Host
				}
			}
		}
		if strings.EqualFold(strings.TrimSuffix(got, "/"), want) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "Forbidden", http.StatusForbidden)
	})
}
func origin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); p == "http" || p == "https" {
		scheme = p
	}
	return strings.ToLower(scheme + "://" + r.Host)
}
