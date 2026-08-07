package assetcleanup

import (
	"context"
	"time"

	"github.com/bitofbytes-io/carma/internal/assets"
)

const DefaultInterval = 7 * 24 * time.Hour

type RunFunc func(context.Context, string) (assets.CleanupReport, error)

func Schedule(ctx context.Context, interval time.Duration, run RunFunc) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	_, _ = run(ctx, TriggerStartup)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = run(ctx, TriggerScheduled)
		}
	}
}
