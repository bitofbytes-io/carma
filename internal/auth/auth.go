package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type Claims struct {
	Subject, Email, Name, Picture string
	EmailVerified                 bool
}
type Google interface {
	AuthURL(string) string
	Exchange(context.Context, string) (Claims, error)
	Allowed(string) bool
}
type GoogleOIDC struct {
	oauth           *oauth2.Config
	verifier        *oidc.IDTokenVerifier
	emails, domains map[string]struct{}
}

func NewGoogleOIDC(ctx context.Context, id, secret, redirect string, emails, domains []string) (*GoogleOIDC, error) {
	p, e := oidc.NewProvider(ctx, "https://accounts.google.com")
	if e != nil {
		return nil, e
	}
	g := &GoogleOIDC{oauth: &oauth2.Config{ClientID: id, ClientSecret: secret, RedirectURL: redirect, Endpoint: google.Endpoint, Scopes: []string{oidc.ScopeOpenID, "email", "profile"}}, verifier: p.Verifier(&oidc.Config{ClientID: id}), emails: map[string]struct{}{}, domains: map[string]struct{}{}}
	for _, v := range emails {
		g.emails[strings.ToLower(strings.TrimSpace(v))] = struct{}{}
	}
	for _, v := range domains {
		g.domains[strings.ToLower(strings.TrimPrefix(strings.TrimSpace(v), "@"))] = struct{}{}
	}
	return g, nil
}
func (g *GoogleOIDC) AuthURL(state string) string {
	return g.oauth.AuthCodeURL(state, oauth2.SetAuthURLParam("prompt", "select_account"))
}
func (g *GoogleOIDC) Exchange(ctx context.Context, code string) (Claims, error) {
	t, e := g.oauth.Exchange(ctx, code)
	if e != nil {
		return Claims{}, fmt.Errorf("token exchange: %w", e)
	}
	raw, ok := t.Extra("id_token").(string)
	if !ok {
		return Claims{}, fmt.Errorf("missing id_token")
	}
	id, e := g.verifier.Verify(ctx, raw)
	if e != nil {
		return Claims{}, fmt.Errorf("verify id_token: %w", e)
	}
	var c struct {
		Sub, Email, Name, Picture string
		EmailVerified             bool `json:"email_verified"`
	}
	if e = id.Claims(&c); e != nil {
		return Claims{}, e
	}
	return Claims{Subject: c.Sub, Email: c.Email, Name: c.Name, Picture: c.Picture, EmailVerified: c.EmailVerified}, nil
}
func (g *GoogleOIDC) Allowed(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if _, ok := g.emails[email]; ok {
		return true
	}
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return false
	}
	_, ok := g.domains[parts[1]]
	return ok
}

type Service struct {
	store repository.Store
	ttl   time.Duration
}

func NewService(store repository.Store, ttl time.Duration) *Service {
	return &Service{store: store, ttl: ttl}
}
func (s *Service) Login(ctx context.Context, c Claims) (model.User, string, error) {
	if strings.TrimSpace(c.Subject) == "" || strings.TrimSpace(c.Email) == "" {
		return model.User{}, "", fmt.Errorf("identity is incomplete")
	}
	now := time.Now().UTC()
	u := model.User{ID: uuid.New(), OAuthProvider: "google", OAuthSubject: c.Subject, Email: strings.ToLower(c.Email), DisplayName: c.Name, AvatarURL: c.Picture, CreatedAt: now, UpdatedAt: now, LastLoginAt: now}
	if existing, err := s.store.FindUserByOAuth(ctx, "google", c.Subject); err != nil {
		return model.User{}, "", err
	} else if existing != nil {
		u.ID, u.CreatedAt = existing.ID, existing.CreatedAt
	} else if existing, err = s.store.FindUserByEmail(ctx, u.Email); err != nil {
		return model.User{}, "", err
	} else if existing != nil {
		u.ID, u.CreatedAt = existing.ID, existing.CreatedAt
	}
	u, e := s.store.UpsertUser(ctx, u)
	if e != nil {
		return u, "", e
	}
	token, e := randomToken()
	if e != nil {
		return u, "", e
	}
	session := model.Session{ID: uuid.New(), UserID: u.ID, CreatedAt: now, ExpiresAt: now.Add(s.ttl)}
	if e = s.store.CreateSession(ctx, session, HashToken(token)); e != nil {
		return u, "", e
	}
	return u, token, nil
}
func (s *Service) DevLogin(ctx context.Context) (model.User, string, error) {
	return s.Login(ctx, Claims{Subject: "local-development", Email: "developer@local.carma", Name: "Local Developer", EmailVerified: true})
}
func (s *Service) Validate(ctx context.Context, token string) (*model.User, error) {
	if token == "" {
		return nil, nil
	}
	sess, u, e := s.store.FindSession(ctx, HashToken(token))
	if e != nil {
		return nil, e
	}
	if sess == nil || u == nil {
		return nil, nil
	}
	if !time.Now().Before(sess.ExpiresAt) {
		_ = s.store.DeleteSession(ctx, sess.ID)
		return nil, nil
	}
	return u, nil
}
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	sess, _, e := s.store.FindSession(ctx, HashToken(token))
	if e != nil || sess == nil {
		return e
	}
	return s.store.DeleteSession(ctx, sess.ID)
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func State() (string, error)    { return randomToken() }
func HashToken(v string) string { x := sha256.Sum256([]byte(v)); return hex.EncodeToString(x[:]) }
