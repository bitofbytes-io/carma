package repository

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const advisoryLockAcquireTimeout = 2 * time.Second

func (p *Postgres) tryAdvisoryLock(ctx context.Context, lockID int64, purpose string) (func(context.Context) error, bool, error) {
	acquireContext, cancelAcquire := boundedAdvisoryLockAcquireContext(ctx)
	defer cancelAcquire()
	connection, err := p.pool.Acquire(acquireContext)
	if err != nil {
		return nil, false, fmt.Errorf("acquire %s lock connection: %w", purpose, err)
	}
	var acquired bool
	if err = connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&acquired); err != nil {
		connection.Release()
		return nil, false, fmt.Errorf("acquire %s advisory lock: %w", purpose, err)
	}
	if !acquired {
		connection.Release()
		return nil, false, nil
	}
	var once sync.Once
	var unlockErr error
	unlock := func(unlockContext context.Context) error {
		once.Do(func() {
			defer connection.Release()
			var unlocked bool
			if err := connection.QueryRow(unlockContext, `SELECT pg_advisory_unlock($1)`, lockID).Scan(&unlocked); err != nil {
				unlockErr = fmt.Errorf("release %s advisory lock: %w", purpose, err)
			} else if !unlocked {
				unlockErr = fmt.Errorf("release %s advisory lock: lock was not held", purpose)
			}
		})
		return unlockErr
	}
	return unlock, true, nil
}

func boundedAdvisoryLockAcquireContext(ctx context.Context) (context.Context, context.CancelFunc) {
	maximumDeadline := time.Now().Add(advisoryLockAcquireTimeout)
	if callerDeadline, ok := ctx.Deadline(); ok && !callerDeadline.After(maximumDeadline) {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, maximumDeadline)
}
