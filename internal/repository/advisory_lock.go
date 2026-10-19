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
// session releases the lock whatever state the session is in.
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
	var once sync.Once
	var unlockErr error
	unlock := func(ctx context.Context) error {
		once.Do(func() {
			if err := connection.Close(ctx); err != nil {
				unlockErr = fmt.Errorf("release %s advisory lock: %w", purpose, err)
			}
		})
		return unlockErr
	}
	return unlock, true, nil
}
