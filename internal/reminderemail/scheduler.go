package reminderemail

import (
	"context"
	"log/slog"
	"time"
)

const DefaultInterval = 24 * time.Hour

type RunFunc func(context.Context, Options) (Report, error)

func Schedule(ctx context.Context, interval time.Duration, run RunFunc, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		report, err := run(ctx, Options{})
		if err != nil && ctx.Err() == nil {
			logger.Error("reminder email run failed", "evaluated", report.Evaluated, "sent", report.Sent, "failed", report.Failed, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
