package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const advisoryLockAcquireTimeout = 2 * time.Second

func (p *Postgres) tryAdvisoryLock(ctx context.Context, lockID int64, purpose string) (func(context.Context) error, bool, error) {
	acquireContext, cancelAcquire := boundedAdvisoryLockAcquireContext(ctx)
	defer cancelAcquire()
	pooledConnection, err := p.pool.Acquire(acquireContext)
	if err != nil {
		return nil, false, fmt.Errorf("acquire %s lock connection: %w", purpose, err)
	}
	connection := &pooledAdvisoryLockConnection{Conn: pooledConnection}
	acquired, err := acquireAdvisoryLock(acquireContext, connection, lockID, purpose)
	if err != nil {
		return nil, false, err
	}
	if !acquired {
		connection.Release()
		return nil, false, nil
	}
	var once sync.Once
	var unlockErr error
	unlock := func(unlockContext context.Context) error {
		once.Do(func() {
			unlockErr = releaseAdvisoryLock(unlockContext, connection, lockID, purpose)
		})
		return unlockErr
	}
	return unlock, true, nil
}

type advisoryLockQueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type advisoryLockConnection interface {
	advisoryLockQueryRower
	Release()
	discardAndClose(context.Context) error
}

type pooledAdvisoryLockConnection struct {
	*pgxpool.Conn
}

func (c *pooledAdvisoryLockConnection) discardAndClose(ctx context.Context) error {
	return c.Hijack().Close(ctx)
}

func queryTryAdvisoryLock(ctx context.Context, queryRower advisoryLockQueryRower, lockID int64) (bool, error) {
	var acquired bool
	err := queryRower.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&acquired)
	return acquired, err
}

func acquireAdvisoryLock(ctx context.Context, connection advisoryLockConnection, lockID int64, purpose string) (bool, error) {
	acquired, err := queryTryAdvisoryLock(ctx, connection, lockID)
	if err != nil {
		acquireErr := fmt.Errorf("acquire %s advisory lock: %w", purpose, err)
		return false, discardAdvisoryLockConnection(ctx, connection, purpose, acquireErr)
	}
	return acquired, nil
}

func releaseAdvisoryLock(ctx context.Context, connection advisoryLockConnection, lockID int64, purpose string) error {
	var unlocked bool
	if err := connection.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, lockID).Scan(&unlocked); err != nil {
		unlockErr := fmt.Errorf("release %s advisory lock: %w", purpose, err)
		return discardAdvisoryLockConnection(ctx, connection, purpose, unlockErr)
	}
	connection.Release()
	if !unlocked {
		return fmt.Errorf("release %s advisory lock: lock was not held", purpose)
	}
	return nil
}

func discardAdvisoryLockConnection(ctx context.Context, connection advisoryLockConnection, purpose string, operationErr error) error {
	if closeErr := connection.discardAndClose(ctx); closeErr != nil {
		return errors.Join(operationErr, fmt.Errorf("discard %s advisory lock connection: %w", purpose, closeErr))
	}
	return operationErr
}

func boundedAdvisoryLockAcquireContext(ctx context.Context) (context.Context, context.CancelFunc) {
	maximumDeadline := time.Now().Add(advisoryLockAcquireTimeout)
	if callerDeadline, ok := ctx.Deadline(); ok && !callerDeadline.After(maximumDeadline) {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, maximumDeadline)
}
