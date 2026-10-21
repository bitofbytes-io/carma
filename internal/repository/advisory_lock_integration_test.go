package repository_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/metrics"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/internal/testdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

	snapshot, err := store.LeaderLocksHeld(t.Context())
	if err != nil || len(snapshot) != 4 {
		t.Fatalf("snapshot=%v err=%v", snapshot, err)
	}
	for purpose, held := range snapshot {
		if !held {
			t.Errorf("%s not held", purpose)
		}
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

func TestLeaderLockContentionReleaseAndDroppedSessionGauge(t *testing.T) {
	scopedURL := testdb.URL(t)
	// Name only connections created for this test. Never terminate a backend
	// merely because it holds one of the application's shared advisory lock IDs.
	appName := "carma_lock_test_" + uuid.NewString()
	ownURL, err := testdb.WithParameter(scopedURL, "application_name", appName)
	if err != nil {
		t.Fatal(err)
	}
	owner := newStoreWithPoolSize(t, ownURL, 4)
	contender := newStoreWithPoolSize(t, scopedURL, 4)
	observer, err := pgx.Connect(t.Context(), scopedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observer.Close(context.Background()) }()
	m := metrics.New()
	m.RegisterLeaderLocks(owner.LeaderLocksHeld)
	assertGauge := func(want string) {
		t.Helper()
		w := httptest.NewRecorder()
		m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if !strings.Contains(w.Body.String(), `carma_leader_locks_held{purpose="asset_cleanup"} `+want) {
			t.Fatalf("missing held gauge %s in %s", want, w.Body.String())
		}
	}
	assertGauge("0")
	unlock, acquired, err := owner.TryAssetCleanupLock(t.Context())
	if err != nil || !acquired {
		t.Fatalf("acquired=%t err=%v", acquired, err)
	}
	t.Cleanup(func() { _ = unlock(context.Background()) })
	assertGauge("1")
	if release, acquired, err := contender.TryAssetCleanupLock(t.Context()); err != nil || acquired || release != nil {
		if release != nil {
			_ = release(t.Context())
		}
		t.Fatalf("contending lock acquired=%t err=%v", acquired, err)
	}
	snapshot, err := contender.LeaderLocksHeld(t.Context())
	if err != nil || snapshot["asset_cleanup"] {
		t.Fatalf("other replica lock leaked into snapshot: %v %v", snapshot, err)
	}
	if err := unlock(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := unlock(t.Context()); err != nil {
		t.Fatalf("repeated release: %v", err)
	}
	assertGauge("0")
	release, acquired, err := contender.TryAssetCleanupLock(t.Context())
	if err != nil || !acquired {
		t.Fatalf("lock not released: acquired=%t err=%v", acquired, err)
	}
	if err := release(t.Context()); err != nil {
		t.Fatal(err)
	}
	unlockDropped, acquired, err := owner.TryAssetCleanupLock(t.Context())
	if err != nil || !acquired {
		t.Fatalf("reacquire=%t err=%v", acquired, err)
	}
	t.Cleanup(func() { _ = unlockDropped(context.Background()) })
	var ownPID int32
	if err := observer.QueryRow(t.Context(), `SELECT a.pid FROM pg_stat_activity a
  WHERE a.application_name = $1 AND a.datname = current_database()
  AND EXISTS (SELECT 1 FROM pg_locks l WHERE l.pid = a.pid AND l.locktype = 'advisory' AND l.granted)`, appName).Scan(&ownPID); err != nil {
		t.Fatal(err)
	}
	var terminated bool
	if err := observer.QueryRow(t.Context(), `SELECT pg_terminate_backend(pid, 5000) FROM pg_stat_activity
  WHERE pid = $1 AND application_name = $2 AND datname = current_database()`, ownPID, appName).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate own test session: terminated=%t err=%v", terminated, err)
	}
	// The owner still tracks this session and has not called unlock. The gauge
	// must observe the vanished PostgreSQL lock rather than the local bookkeeping.
	assertGauge("0")
	release, acquired, err = contender.TryAssetCleanupLock(t.Context())
	if err != nil || !acquired {
		t.Fatalf("drop retained lock: acquired=%t err=%v", acquired, err)
	}
	defer func() { _ = release(context.Background()) }()
	assertGauge("0")
	// Repeated scrapes may race with release without accessing pgx.Conn.
	var wait sync.WaitGroup
	for range 4 {
		wait.Go(func() { _, _ = owner.LeaderLocksHeld(t.Context()) })
	}
	_ = unlockDropped(t.Context())
	wait.Wait()
}
