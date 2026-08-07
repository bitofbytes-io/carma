package repository

import (
	"context"
	"errors"
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

type advisoryLockResultRow struct {
	unlocked bool
	err      error
}

func (r advisoryLockResultRow) Scan(destinations ...any) error {
	if r.err != nil {
		return r.err
	}
	*destinations[0].(*bool) = r.unlocked
	return nil
}

type recordingAdvisoryLockConnection struct {
	row                pgx.Row
	releases, discards int
	discardErr         error
}

func (c *recordingAdvisoryLockConnection) QueryRow(context.Context, string, ...any) pgx.Row {
	return c.row
}

func (c *recordingAdvisoryLockConnection) Release() {
	c.releases++
}

func (c *recordingAdvisoryLockConnection) discardAndClose(context.Context) error {
	c.discards++
	return c.discardErr
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

func TestReleaseAdvisoryLockDiscardsConnectionOnQueryError(t *testing.T) {
	queryErr := errors.New("unlock query failed")
	connection := &recordingAdvisoryLockConnection{row: advisoryLockResultRow{err: queryErr}}

	err := releaseAdvisoryLock(t.Context(), connection, assetCleanupAdvisoryLockID, "asset cleanup")
	if !errors.Is(err, queryErr) {
		t.Fatalf("release error = %v", err)
	}
	if connection.discards != 1 || connection.releases != 0 {
		t.Fatalf("discards=%d releases=%d", connection.discards, connection.releases)
	}
}

func TestAcquireAdvisoryLockDiscardsConnectionOnQueryError(t *testing.T) {
	queryErr := errors.New("acquire query failed")
	connection := &recordingAdvisoryLockConnection{row: advisoryLockResultRow{err: queryErr}}

	acquired, err := acquireAdvisoryLock(t.Context(), connection, assetCleanupAdvisoryLockID, "asset cleanup")
	if acquired || !errors.Is(err, queryErr) {
		t.Fatalf("acquired=%t error=%v", acquired, err)
	}
	if connection.discards != 1 || connection.releases != 0 {
		t.Fatalf("discards=%d releases=%d", connection.discards, connection.releases)
	}
}

func TestAcquireAdvisoryLockReturnsQueryAndDiscardErrors(t *testing.T) {
	queryErr := errors.New("acquire query failed")
	discardErr := errors.New("connection close failed")
	connection := &recordingAdvisoryLockConnection{row: advisoryLockResultRow{err: queryErr}, discardErr: discardErr}

	acquired, err := acquireAdvisoryLock(t.Context(), connection, assetCleanupAdvisoryLockID, "asset cleanup")
	if acquired || !errors.Is(err, queryErr) || !errors.Is(err, discardErr) {
		t.Fatalf("acquired=%t error=%v", acquired, err)
	}
	if connection.discards != 1 || connection.releases != 0 {
		t.Fatalf("discards=%d releases=%d", connection.discards, connection.releases)
	}
}

func TestReleaseAdvisoryLockReleasesConnectionOnSuccess(t *testing.T) {
	connection := &recordingAdvisoryLockConnection{row: advisoryLockResultRow{unlocked: true}}

	if err := releaseAdvisoryLock(t.Context(), connection, assetCleanupAdvisoryLockID, "asset cleanup"); err != nil {
		t.Fatal(err)
	}
	if connection.releases != 1 || connection.discards != 0 {
		t.Fatalf("releases=%d discards=%d", connection.releases, connection.discards)
	}
}

func TestReleaseAdvisoryLockReportsNotHeldAfterSafeRelease(t *testing.T) {
	connection := &recordingAdvisoryLockConnection{row: advisoryLockResultRow{unlocked: false}}

	err := releaseAdvisoryLock(t.Context(), connection, assetCleanupAdvisoryLockID, "asset cleanup")
	if err == nil || err.Error() != "release asset cleanup advisory lock: lock was not held" {
		t.Fatalf("release error = %v", err)
	}
	if connection.releases != 1 || connection.discards != 0 {
		t.Fatalf("releases=%d discards=%d", connection.releases, connection.discards)
	}
}

func TestReleaseAdvisoryLockReturnsQueryAndDiscardErrors(t *testing.T) {
	queryErr := errors.New("unlock query failed")
	discardErr := errors.New("connection close failed")
	connection := &recordingAdvisoryLockConnection{row: advisoryLockResultRow{err: queryErr}, discardErr: discardErr}

	err := releaseAdvisoryLock(t.Context(), connection, assetCleanupAdvisoryLockID, "asset cleanup")
	if !errors.Is(err, queryErr) || !errors.Is(err, discardErr) {
		t.Fatalf("release error = %v", err)
	}
	if connection.discards != 1 || connection.releases != 0 {
		t.Fatalf("discards=%d releases=%d", connection.discards, connection.releases)
	}
}
