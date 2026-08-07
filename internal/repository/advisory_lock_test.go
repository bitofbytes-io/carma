package repository

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type recordingAdvisoryLockQuery struct {
	ctx    context.Context
	lockID int64
}

func (q *recordingAdvisoryLockQuery) QueryRow(ctx context.Context, _ string, args ...any) pgx.Row {
	q.ctx = ctx
	q.lockID = args[0].(int64)
	return advisoryLockBoolRow(true)
}

type advisoryLockBoolRow bool

func (r advisoryLockBoolRow) Scan(destinations ...any) error {
	*destinations[0].(*bool) = bool(r)
	return nil
}

func TestAdvisoryLockQueryUsesBoundedContext(t *testing.T) {
	bounded, cancel := boundedAdvisoryLockAcquireContext(context.Background())
	defer cancel()
	query := &recordingAdvisoryLockQuery{}

	acquired, err := queryTryAdvisoryLock(bounded, query, assetCleanupAdvisoryLockID)
	if err != nil || !acquired {
		t.Fatalf("acquired=%t err=%v", acquired, err)
	}
	deadline, ok := query.ctx.Deadline()
	if !ok {
		t.Fatal("advisory-lock query context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > advisoryLockAcquireTimeout {
		t.Fatalf("query deadline remaining = %v", remaining)
	}
	if query.lockID != assetCleanupAdvisoryLockID {
		t.Fatalf("lock ID = %x, want %x", query.lockID, assetCleanupAdvisoryLockID)
	}
}
