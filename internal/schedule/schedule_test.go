package schedule

import (
	"context"
	"testing"
	"time"
)

func TestEveryRunsAtStartRepeatsAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runs := make(chan struct{}, 4)
	done := make(chan struct{})
	go func() {
		Every(ctx, 10*time.Millisecond, func(context.Context) {
			select {
			case runs <- struct{}{}:
			default:
			}
		})
		close(done)
	}()
	for range 2 {
		select {
		case <-runs:
		case <-time.After(time.Second):
			t.Fatal("scheduled run did not occur")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after cancellation")
	}
}

func TestEveryRunsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runs := make(chan struct{}, 1)
	go Every(ctx, time.Hour, func(context.Context) { runs <- struct{}{} })
	select {
	case <-runs:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not run immediately")
	}
}
