package repository

import (
	"context"
	"testing"
	"time"
)

func TestReminderLockAcquireContextIsBounded(t *testing.T) {
	ctx, cancel := boundedReminderLockAcquireContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("bounded acquire context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > reminderLockAcquireTimeout {
		t.Fatalf("bounded deadline remaining = %v", remaining)
	}
}

func TestReminderLockAcquireContextPreservesEarlierCallerDeadline(t *testing.T) {
	caller, cancelCaller := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelCaller()
	callerDeadline, _ := caller.Deadline()
	ctx, cancel := boundedReminderLockAcquireContext(caller)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.Equal(callerDeadline) {
		t.Fatalf("deadline = %v, want caller deadline %v", deadline, callerDeadline)
	}
}
