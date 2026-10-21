package repository

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const advisoryLockAcquireTimeout = 2 * time.Second

// tryAdvisoryLock takes a session-level advisory lock on its own connection,
// opened with the pool's settings but outside the pool. A leader job holds its
// lock for the whole run while its queries go through the pool, so a lock never
// takes a pool slot: with every lock held, the pool can still serve the jobs,
// HTTP, and session cleanup. Unlocking closes the connection, and ending the
// session releases the lock whatever state the session is in. A dropped session
// can release the lock while the job continues: this is not an exactly-once
// guarantee. LeaderLocksHeld observes that loss but does not cancel the job.
func (p *Postgres) tryAdvisoryLock(ctx context.Context, lockID int64, purpose string) (func(context.Context) error, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, advisoryLockAcquireTimeout)
	defer cancel()
	connection, err := pgx.ConnectConfig(ctx, p.pool.Config().ConnConfig)
	if err != nil {
		return nil, false, fmt.Errorf("connect for %s lock: %w", purpose, err)
	}
	var acquired bool
	if err = connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&acquired); err != nil {
		// The lock may have been taken before the error, so end the session.
		_ = connection.Close(ctx)
		return nil, false, fmt.Errorf("acquire %s advisory lock: %w", purpose, err)
	}
	if !acquired {
		_ = connection.Close(ctx)
		return nil, false, nil
	}
	pid := connection.PgConn().PID()
	p.leaderMu.Lock()
	if p.leaderSessions == nil {
		p.leaderSessions = make(map[int64]uint32)
	}
	p.leaderSessions[lockID] = pid
	p.leaderMu.Unlock()
	var once sync.Once
	var unlockErr error
	unlock := func(ctx context.Context) error {
		once.Do(func() {
			if err := connection.Close(ctx); err != nil {
				unlockErr = fmt.Errorf("release %s advisory lock: %w", purpose, err)
			}
			p.leaderMu.Lock()
			if p.leaderSessions[lockID] == pid {
				delete(p.leaderSessions, lockID)
			}
			p.leaderMu.Unlock()
		})
		return unlockErr
	}
	return unlock, true, nil
}

// LeaderLocksHeld reports the actual database locks owned by this instance's
// dedicated sessions. Query failures return no snapshot, never false zeroes.
// The query and pool wait share a short deadline and never touch a lock session
// concurrently with its release.
func (p *Postgres) LeaderLocksHeld(ctx context.Context) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	locks := []struct {
		id      int64
		purpose string
	}{
		{assetCleanupAdvisoryLockID, "asset_cleanup"},
		{reminderEmailAdvisoryLockID, "reminder_email"},
		{reminderDigestAdvisoryLockID, "reminder_digest"},
		{recallCheckAdvisoryLockID, "recall_check"},
	}
	pids, high, low := make([]int64, len(locks)), make([]int64, len(locks)), make([]int64, len(locks))
	p.leaderMu.Lock()
	for i, lock := range locks {
		pids[i] = int64(p.leaderSessions[lock.id])
		high[i] = int64(uint64(lock.id) >> 32)
		low[i] = int64(uint64(lock.id) & 0xffffffff)
	}
	p.leaderMu.Unlock()
	rows, err := p.pool.Query(ctx, `SELECT wanted.ordinality, EXISTS (
  SELECT 1 FROM pg_locks held
  WHERE held.locktype = 'advisory' AND held.granted
   AND held.pid = wanted.pid AND held.classid = wanted.high::oid
   AND held.objid = wanted.low::oid AND held.objsubid = 1
   AND held.database = (SELECT oid FROM pg_database WHERE datname = current_database())
 ) FROM unnest($1::bigint[], $2::bigint[], $3::bigint[]) WITH ORDINALITY AS wanted(pid, high, low, ordinality)`, pids, high, low)
	if err != nil {
		return nil, fmt.Errorf("query leader locks: %w", err)
	}
	defer rows.Close()
	result := make(map[string]bool, len(locks))
	for rows.Next() {
		var ordinal int
		var held bool
		if err := rows.Scan(&ordinal, &held); err != nil {
			return nil, err
		}
		result[locks[ordinal-1].purpose] = held
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
