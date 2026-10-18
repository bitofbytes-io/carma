package metrics

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// poolCollector reads a pgxpool.Stat snapshot on each scrape.
type poolCollector struct {
	stat func() *pgxpool.Stat

	acquired, idle, total, max, constructing  *prometheus.Desc
	acquires, emptyAcquires, canceledAcquires *prometheus.Desc
	acquireSeconds, emptyAcquireWaitSeconds   *prometheus.Desc
	newConns                                  *prometheus.Desc
}

func newPoolCollector(stat func() *pgxpool.Stat) *poolCollector {
	desc := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc("pgxpool_"+name, help, nil, nil)
	}
	return &poolCollector{
		stat:                    stat,
		acquired:                desc("acquired_conns", "Connections currently acquired from the pool."),
		idle:                    desc("idle_conns", "Idle connections in the pool."),
		total:                   desc("total_conns", "Connections in the pool, acquired, idle, or being constructed."),
		max:                     desc("max_conns", "Maximum pool size."),
		constructing:            desc("constructing_conns", "Connections being established."),
		acquires:                desc("acquires_total", "Successful connection acquires."),
		emptyAcquires:           desc("empty_acquires_total", "Acquires that waited because the pool had no idle connection."),
		canceledAcquires:        desc("canceled_acquires_total", "Acquires canceled by their context."),
		acquireSeconds:          desc("acquire_duration_seconds_total", "Total time spent in successful acquires."),
		emptyAcquireWaitSeconds: desc("empty_acquire_wait_seconds_total", "Total time successful acquires waited for a connection to free up or be created."),
		newConns:                desc("new_conns_total", "Connections opened by the pool."),
	}
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		c.acquired, c.idle, c.total, c.max, c.constructing,
		c.acquires, c.emptyAcquires, c.canceledAcquires,
		c.acquireSeconds, c.emptyAcquireWaitSeconds, c.newConns,
	} {
		ch <- d
	}
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.stat()
	if s == nil {
		return
	}
	gauge := func(d *prometheus.Desc, v float64) { ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v) }
	counter := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v)
	}
	gauge(c.acquired, float64(s.AcquiredConns()))
	gauge(c.idle, float64(s.IdleConns()))
	gauge(c.total, float64(s.TotalConns()))
	gauge(c.max, float64(s.MaxConns()))
	gauge(c.constructing, float64(s.ConstructingConns()))
	counter(c.acquires, float64(s.AcquireCount()))
	counter(c.emptyAcquires, float64(s.EmptyAcquireCount()))
	counter(c.canceledAcquires, float64(s.CanceledAcquireCount()))
	counter(c.acquireSeconds, s.AcquireDuration().Seconds())
	counter(c.emptyAcquireWaitSeconds, s.EmptyAcquireWaitTime().Seconds())
	counter(c.newConns, float64(s.NewConnsCount()))
}
