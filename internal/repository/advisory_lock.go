package repository

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const advisoryLockAcquireTimeout = 2 * time.Second

// tryAdvisoryLock takes a session-level advisory lock on a dedicated pooled
// connection. Unlocking closes that connection instead of returning it to the
// pool: ending the session releases the lock, so the pool never hands out a
// connection that still holds it, whatever state the session is in.
func (p *Postgres) tryAdvisoryLock(ctx context.Context, lockID int64, purpose string) (func(context.Context) error, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, advisoryLockAcquireTimeout)
	defer cancel()
	connection, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire %s lock connection: %w", purpose, err)
	}
	var acquired bool
	if err = connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&acquired); err != nil {
		// The lock may have been taken before the error, so discard the session.
		_ = connection.Hijack().Close(ctx)
		return nil, false, fmt.Errorf("acquire %s advisory lock: %w", purpose, err)
	}
	if !acquired {
		connection.Release()
		return nil, false, nil
	}
	var once sync.Once
	var unlockErr error
	unlock := func(ctx context.Context) error {
		once.Do(func() {
			if err := connection.Hijack().Close(ctx); err != nil {
				unlockErr = fmt.Errorf("release %s advisory lock: %w", purpose, err)
			}
		})
		return unlockErr
	}
	return unlock, true, nil
}
