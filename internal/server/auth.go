package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/config"
	"github.com/bitofbytes-io/carma/internal/middleware"
)

const (
	oauthStateCookie     = "carma_oauth_state"
	loginErrorExpired    = "expired"
	loginErrorOAuth      = "oauth"
	loginErrorNotInvited = "not-invited"
)

func (s *Server) loginPage(response http.ResponseWriter, request *http.Request) {
	target, _ := safeRedirectTarget(request.URL.Query().Get("redirect"))
	data := pageData{
		Title:       "Sign in",
		Development: s.cfg.AuthMode == config.AuthDevelopment,
		Error:       loginErrorMessage(request.URL.Query().Get("error")),
		Redirect:    target,
	}
	s.render(response, http.StatusOK, "login", data)
}

func (s *Server) devLogin(response http.ResponseWriter, request *http.Request) {
	if s.cfg.AuthMode != config.AuthDevelopment || strings.EqualFold(s.cfg.AppEnv, "production") {
		http.NotFound(response, request)
		return
	}
	_, token, err := s.auth.DevLogin(request.Context())
	if err != nil {
		s.fail(response, err)
		return
	}
	middleware.SetSession(response, token, s.cfg.SecureCookies(), s.cfg.SessionTTL)
	http.Redirect(response, request, "/", http.StatusSeeOther)
}

func (s *Server) oauthStart(response http.ResponseWriter, request *http.Request) {
	if s.cfg.AuthMode != config.AuthGoogle || s.google == nil {
		http.NotFound(response, request)
		return
	}
	nonce, err := auth.State()
	if err != nil {
		s.fail(response, err)
		return
	}
	target, _ := safeRedirectTarget(request.URL.Query().Get("redirect"))
	state, err := signedOAuthState(nonce, target, s.cfg.GoogleClientSecret)
	if err != nil {
		s.fail(response, err)
		return
	}
	http.SetCookie(response, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    state,
		Path:     "/api/auth/google/callback",
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	http.Redirect(response, request, s.google.AuthURL(state), http.StatusFound)
}

func (s *Server) oauthCallback(response http.ResponseWriter, request *http.Request) {
	cookie, err := request.Cookie(oauthStateCookie)
	state := request.URL.Query().Get("state")
	target, validState := parseOAuthState(state, s.cfg.GoogleClientSecret)
	if err != nil || cookie.Value == "" || !hmac.Equal([]byte(state), []byte(cookie.Value)) || !validState {
		http.Redirect(response, request, loginErrorLocation(loginErrorExpired), http.StatusSeeOther)
		return
	}
	http.SetCookie(response, &http.Cookie{
		Name:     oauthStateCookie,
		Path:     "/api/auth/google/callback",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
	claims, err := s.google.Exchange(request.Context(), request.URL.Query().Get("code"))
	if err != nil {
		http.Redirect(response, request, loginErrorLocation(loginErrorOAuth), http.StatusSeeOther)
		return
	}
	if !claims.EmailVerified || !s.google.Allowed(claims.Email) {
		http.Redirect(response, request, loginErrorLocation(loginErrorNotInvited), http.StatusSeeOther)
		return
	}
	_, token, err := s.auth.Login(request.Context(), claims)
	if err != nil {
		s.fail(response, err)
		return
	}
	middleware.SetSession(response, token, s.cfg.SecureCookies(), s.cfg.SessionTTL)
	http.Redirect(response, request, target, http.StatusSeeOther)
}

func loginErrorLocation(code string) string {
	return "/login?error=" + url.QueryEscape(code)
}

func loginErrorMessage(code string) string {
	switch code {
	case loginErrorExpired:
		return "Sign-in expired. Please try again."
	case loginErrorOAuth:
		return "Google sign-in could not be completed. Please try again."
	case loginErrorNotInvited:
		return "This verified Google account is not invited to Carma."
	default:
		return ""
	}
}

func (s *Server) logout(response http.ResponseWriter, request *http.Request) {
	if cookie, err := request.Cookie(middleware.CookieName); err == nil {
		if err = s.auth.Logout(request.Context(), cookie.Value); err != nil {
			s.fail(response, err)
			return
		}
	}
	middleware.ClearSession(response, s.cfg.SecureCookies())
	http.Redirect(response, request, "/login", http.StatusSeeOther)
}

type oauthStatePayload struct {
	Nonce    string `json:"n"`
	Redirect string `json:"r"`
}

func signedOAuthState(nonce, redirect, secret string) (string, error) {
	payload, err := json.Marshal(oauthStatePayload{Nonce: nonce, Redirect: redirect})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func parseOAuthState(state, secret string) (string, bool) {
	parts := strings.Split(state, ".")
	if len(parts) != 2 {
		return "/", false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "/", false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "/", false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "/", false
	}
	var payload oauthStatePayload
	if err = json.Unmarshal(payloadBytes, &payload); err != nil || payload.Nonce == "" {
		return "/", false
	}
	target, ok := safeRedirectTarget(payload.Redirect)
	if !ok {
		return "/", false
	}
	return target, true
}

func safeRedirectTarget(raw string) (string, bool) {
	if raw == "" {
		return "/", true
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.Contains(raw, "\\") {
		return "/", false
	}
	for _, character := range raw {
		if character < 0x20 || character == 0x7f {
			return "/", false
		}
	}
	target, err := url.ParseRequestURI(raw)
	if err != nil || target.IsAbs() || target.Host != "" || !strings.HasPrefix(target.Path, "/") || strings.HasPrefix(target.Path, "//") || strings.Contains(target.Path, "\\") {
		return "/", false
	}
	decoded, err := url.PathUnescape(target.EscapedPath())
	if err != nil || strings.Contains(decoded, "\\") || strings.HasPrefix(decoded, "//") {
		return "/", false
	}
	return raw, true
}
