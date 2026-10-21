package metrics

import (
	"context"
	"github.com/prometheus/client_golang/prometheus"
	"time"
)

// RegisterLeaderLocks exports a fresh observation of the replica's dedicated
// lock sessions. A failed observation omits the series instead of claiming zero.
func (m *Metrics) RegisterLeaderLocks(snapshot func(context.Context) (map[string]bool, error)) {
	if m == nil {
		return
	}
	m.registry.MustRegister(&leaderLockCollector{
		snapshot: snapshot,
		desc:     prometheus.NewDesc("carma_leader_locks_held", "Leader locks actually held by this replica's dedicated PostgreSQL sessions (0 or 1). Omitted when the database cannot be queried.", []string{"purpose"}, nil),
	})
}

type leaderLockCollector struct {
	snapshot func(context.Context) (map[string]bool, error)
	desc     *prometheus.Desc
}

func (c *leaderLockCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }
func (c *leaderLockCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snapshot, err := c.snapshot(ctx)
	if err != nil {
		return
	}
	// Keep labels bounded even if a future snapshot adds an unexpected purpose.
	for _, purpose := range []string{"asset_cleanup", "reminder_email", "reminder_digest", "recall_check"} {
		held, ok := snapshot[purpose]
		if !ok {
			continue
		}
		value := 0.0
		if held {
			value = 1
		}
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, value, purpose)
	}
}
