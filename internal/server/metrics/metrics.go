// Package metrics exposes the Prometheus instrumentation shared by the HTTP
// API and the gRPC channel layer.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	runsTotal       *prometheus.CounterVec
	runDuration     *prometheus.HistogramVec
	agentsOnline    prometheus.Gauge
	queueDepth      prometheus.Gauge
	repoLastCheck   *prometheus.GaugeVec
	grpcReconnects  prometheus.Counter
	registry        *prometheus.Registry
}

func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		runsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bmc_runs_total", Help: "Runs by operation and terminal status.",
		}, []string{"operation", "status"}),
		runDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "bmc_run_duration_seconds",
			Help:    "Wall-clock duration of terminal runs.",
			Buckets: []float64{1, 5, 15, 30, 60, 300, 900, 3600, 14400},
		}, []string{"operation", "status"}),
		agentsOnline: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "bmc_agents_online", Help: "Agents with a live control stream.",
		}),
		queueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "bmc_dispatch_queue_depth", Help: "Runs waiting for dispatch.",
		}),
		repoLastCheck: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "bmc_repository_last_check_timestamp", Help: "Last successful restic check per repository (unix seconds).",
		}, []string{"repository_id"}),
		grpcReconnects: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "bmc_agent_grpc_reconnects_total", Help: "Agent-side reconnect attempts observed server-side (stream (re)starts).",
		}),
		registry: reg,
	}
	reg.MustRegister(m.runsTotal, m.runDuration, m.agentsOnline, m.queueDepth, m.repoLastCheck, m.grpcReconnects)
	return m
}

// ObserveRun records a terminal run.
func (m *Metrics) ObserveRun(operation, status string, d time.Duration) {
	m.runsTotal.WithLabelValues(operation, status).Inc()
	m.runDuration.WithLabelValues(operation, status).Observe(d.Seconds())
}

func (m *Metrics) SetAgentsOnline(n int)      { m.agentsOnline.Set(float64(n)) }
func (m *Metrics) SetQueueDepth(n int)        { m.queueDepth.Set(float64(n)) }
func (m *Metrics) IncReconnects()             { m.grpcReconnects.Inc() }
func (m *Metrics) SetRepoCheck(id string, t time.Time) {
	m.repoLastCheck.WithLabelValues(id).Set(float64(t.Unix()))
}

// Handler serves the Prometheus text exposition.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
