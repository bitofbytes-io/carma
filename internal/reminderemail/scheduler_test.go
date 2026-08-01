package reminderemail

import (
	"context"
	"testing"
	"time"
)

func TestScheduleRunsImmediatelyAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	runs := make(chan struct{}, 1)
	go func() {
		Schedule(ctx, time.Hour, func(context.Context, Options) (Report, error) {
			runs <- struct{}{}
			return Report{}, nil
		}, nil)
		close(done)
	}()
	select {
	case <-runs:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not run immediately")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after cancellation")
	}
}
