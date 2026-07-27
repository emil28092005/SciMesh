package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Stats is a point-in-time snapshot of the coordinator's domain state: counts of
// tasks, jobs, and workers keyed by their status. Maps are expected to be
// zero-filled by the provider so every known status is always present, giving
// the dashboard flat zero lines instead of gaps.
type Stats struct {
	Tasks   map[string]int
	Jobs    map[string]int
	Workers map[string]int
}

// StatsFunc returns the current snapshot. It is called on every scrape, so it
// must be a cheap aggregate query.
type StatsFunc func(context.Context) (Stats, error)

// RegisterBusiness registers a collector that reports domain-state gauges
// (scimesh_tasks/jobs/workers by status) sourced from collect on each scrape.
// Deriving the gauges at scrape time keeps them fresh without a background
// goroutine, and a failed query simply yields no samples for that scrape.
func (m *Metrics) RegisterBusiness(collect StatsFunc) {
	m.reg.MustRegister(&businessCollector{
		collect: collect,
		tasks:   prometheus.NewDesc("scimesh_tasks", "Tasks by status.", []string{"status"}, nil),
		jobs:    prometheus.NewDesc("scimesh_jobs", "Jobs by status.", []string{"status"}, nil),
		workers: prometheus.NewDesc("scimesh_workers", "Workers by status.", []string{"status"}, nil),
	})
}

type businessCollector struct {
	collect              StatsFunc
	tasks, jobs, workers *prometheus.Desc
}

func (c *businessCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.tasks
	ch <- c.jobs
	ch <- c.workers
}

func (c *businessCollector) Collect(ch chan<- prometheus.Metric) {
	// A bounded query so one slow scrape cannot stall Prometheus.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	s, err := c.collect(ctx)
	if err != nil {
		return // no samples this scrape; Prometheus keeps the last value
	}
	emit(ch, c.tasks, s.Tasks)
	emit(ch, c.jobs, s.Jobs)
	emit(ch, c.workers, s.Workers)
}

func emit(ch chan<- prometheus.Metric, desc *prometheus.Desc, counts map[string]int) {
	for status, n := range counts {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(n), status)
	}
}
