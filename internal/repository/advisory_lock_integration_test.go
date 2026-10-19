package repository_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/internal/testdb"
)

// TestLeaderLocksLeavePoolForQueries is the BOB-171 regression. A replica that
// wins every leader lock must still serve HTTP and run each job's queries. The
// locks used to hold a pool connection each, so with as many locks as pool
// slots every later query waited forever and no lock was ever released.
func TestLeaderLocksLeavePoolForQueries(t *testing.T) {
	scopedURL := testdb.URL(t)
	store := newStoreWithPoolSize(t, scopedURL, 4)
	locks := []struct {
		name string
		try  func(context.Context) (func(context.Context) error, bool, error)
	}{
		{"asset cleanup", store.TryAssetCleanupLock},
		{"reminder email", store.TryReminderEmailLock},
		{"reminder digest", store.TryReminderDigestLock},
		{"recall check", store.TryRecallCheckLock},
	}
	if maxConns := int(store.PoolStat().MaxConns()); maxConns != len(locks) {
		t.Fatalf("pool max conns = %d, want one per leader lock (%d)", maxConns, len(locks))
	}
	for _, lock := range locks {
		unlock, acquired, err := lock.try(context.Background())
		if err != nil || !acquired {
			t.Fatalf("%s lock acquired=%t err=%v", lock.name, acquired, err)
		}
		t.Cleanup(func() { _ = unlock(context.Background()) })
	}

	// Each job's next query, the readiness ping, and a sign-in lookup all run
	// at once, as they would on a replica that just won every lock.
	queries := map[string]func(context.Context) error{
		"readiness ping": store.Ping,
		"sign-in lookup": func(ctx context.Context) error {
			_, err := store.FindUserByEmail(ctx, "nobody@example.com")
			return err
		},
		"session cleanup": func(ctx context.Context) error {
			_, err := store.DeleteExpiredSessions(ctx, time.Now())
			return err
		},
		"asset cleanup": func(ctx context.Context) error {
			_, err := store.ListReferencedAssetKeys(ctx)
			return err
		},
		"reminder email": func(ctx context.Context) error {
			_, err := store.ListReminders(ctx, nil, false)
			return err
		},
		"reminder digest": func(ctx context.Context) error {
			_, err := store.ReminderDigestClaimed(ctx, time.Now())
			return err
		},
		"recall check": func(ctx context.Context) error {
			_, err := store.ListRecallCheckDue(ctx, time.Now())
			return err
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wait sync.WaitGroup
	var mu sync.Mutex
	failures := map[string]error{}
	for name, query := range queries {
		wait.Go(func() {
			if err := query(ctx); err != nil {
				mu.Lock()
				failures[name] = err
				mu.Unlock()
			}
		})
	}
	wait.Wait()
	for name, err := range failures {
		t.Errorf("%s with every leader lock held: %v", name, err)
	}
	if acquired := store.PoolStat().AcquiredConns(); acquired != 0 {
		t.Errorf("leader locks hold %d pool connections, want 0", acquired)
	}
}

func newStoreWithPoolSize(t *testing.T, scopedURL string, size int) *repository.Postgres {
	t.Helper()
	limitedURL, err := testdb.WithParameter(scopedURL, "pool_max_conns", strconv.Itoa(size))
	if err != nil {
		t.Fatal(err)
	}
	store, err := repository.NewPostgres(context.Background(), limitedURL)
	if err != nil {
		t.Fatal(err)
	}
	// Registered first, so it runs after the lock cleanups.
	t.Cleanup(store.Close)
	return store
}
