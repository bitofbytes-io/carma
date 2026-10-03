package auth

import (
	"context"
	"sync"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/google/uuid"
)

// fakeSessions keeps users and sessions in memory for Service tests. It
// implements only the user and session methods; any other Store method panics.
type fakeSessions struct {
	repository.Store
	mu       sync.Mutex
	users    map[uuid.UUID]model.User
	sessions map[string]model.Session // by token hash
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{users: map[uuid.UUID]model.User{}, sessions: map[string]model.Session{}}
}

func (f *fakeSessions) FindUserByOAuth(_ context.Context, provider, subject string) (*model.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.OAuthProvider == provider && u.OAuthSubject == subject {
			return &u, nil
		}
	}
	return nil, nil
}

func (f *fakeSessions) FindUserByEmail(_ context.Context, email string) (*model.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.Email == email {
			return &u, nil
		}
	}
	return nil, nil
}

func (f *fakeSessions) UpsertUser(_ context.Context, u model.User) (model.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[u.ID] = u
	return u, nil
}

func (f *fakeSessions) CreateSession(_ context.Context, s model.Session, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[hash] = s
	return nil
}

func (f *fakeSessions) FindSession(_ context.Context, hash string) (*model.Session, *model.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[hash]
	if !ok {
		return nil, nil, nil
	}
	u := f.users[s.UserID]
	return &s, &u, nil
}

func (f *fakeSessions) DeleteSession(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for hash, s := range f.sessions {
		if s.ID == id {
			delete(f.sessions, hash)
		}
	}
	return nil
}

func (f *fakeSessions) DeleteExpiredSessions(_ context.Context, now time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var deleted int64
	for hash, s := range f.sessions {
		if !now.Before(s.ExpiresAt) {
			delete(f.sessions, hash)
			deleted++
		}
	}
	return deleted, nil
}
