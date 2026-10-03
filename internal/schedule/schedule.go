// Package schedule runs background jobs on a fixed interval.
package schedule

import (
	"context"
	"time"
)

// Every runs fn immediately and then once per interval until ctx is canceled.
// Runs never overlap: the next tick waits for the current run to return.
func Every(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		fn(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
