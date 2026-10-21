// Package metrics holds the Prometheus collectors of every PandaEye role and the /metrics + pprof
// server on PE_METRICS_LISTEN. Labels never carry tenant or server ids (ARCHITECTURE 5.3); label
// values are bounded enums (stages, results, reasons, check ids, consumer names).
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Probe RTT stages (label "stage").
const (
	StageGatewayToWorker  = "gateway_worker"  // gateway receive -> worker emit
	StageWorkerToVerdict  = "worker_verdict"  // worker emit -> verdict service
	StageVerdictToGateway = "verdict_gateway" // verdict service -> gateway send
	StageCloud            = "cloud"           // gateway receive -> gateway send (whole cloud path)
	StagePlugin           = "plugin"          // plugin-measured end to end (Ping.last_probe_rtt_us); the SLO
)

// Batch results (label "result" of Batches).
const (
	BatchStored    = "stored"
	BatchDuplicate = "duplicate"
	BatchTooLarge  = "too_large"
	BatchInvalid   = "invalid"
)

// Metrics is the process-wide set of collectors. Components register additional collectors on
// Registry.
type Metrics struct {
	Registry *prometheus.Registry

	ProbeRTT          *prometheus.HistogramVec // stage
	Batches           *prometheus.CounterVec   // result
	Events            prometheus.Counter       // events stored by gateways
	BatchBytes        prometheus.Counter       // received batch bytes on the wire (compressed)
	Drops             *prometheus.CounterVec   // reason (overflow, stale, erased, ...)
	Auth              *prometheus.CounterVec   // result (ok, invalid, limit, duplicate, rate_limited, unavailable)
	Connections       prometheus.Gauge         // open plugin connections
	ConnectionsClosed *prometheus.CounterVec   // reason (close code name)
	ConsumerLag       *prometheus.GaugeVec     // consumer: messages pending in the stream
	LeasesHeld        *prometheus.GaugeVec     // group
	LeaseChanges      *prometheus.CounterVec   // group, event (acquired, released, lost)
	Signals           *prometheus.CounterVec   // check
	Verdicts          *prometheus.CounterVec   // action, shadow
	PublishErrors     *prometheus.CounterVec   // stream
}

// New creates and registers every collector, plus the Go and process collectors.
func New() *Metrics {
	r := prometheus.NewRegistry()
	f := func(c prometheus.Collector) { r.MustRegister(c) }
	m := &Metrics{Registry: r}
	m.ProbeRTT = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "pandaeye", Name: "probe_rtt_seconds",
		Help:    "Probe latency by pipeline stage.",
		Buckets: []float64{.001, .0025, .005, .01, .02, .035, .05, .075, .1, .15, .25, .5, 1, 2.5, 5},
	}, []string{"stage"})
	m.Batches = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pandaeye", Name: "gateway_batches_total", Help: "Batches handled by the gateway by result.",
	}, []string{"result"})
	m.Events = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pandaeye", Name: "gateway_events_total", Help: "Events stored in the event stream.",
	})
	m.BatchBytes = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pandaeye", Name: "gateway_batch_bytes_total", Help: "Batch bytes received on the wire.",
	})
	m.Drops = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pandaeye", Name: "dropped_total", Help: "Items dropped by reason.",
	}, []string{"reason"})
	m.Auth = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pandaeye", Name: "gateway_auth_total", Help: "Hello outcomes.",
	}, []string{"result"})
	m.Connections = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "pandaeye", Name: "gateway_connections", Help: "Open plugin connections.",
	})
	m.ConnectionsClosed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pandaeye", Name: "gateway_connections_closed_total", Help: "Closed plugin connections by reason.",
	}, []string{"reason"})
	m.ConsumerLag = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "pandaeye", Name: "consumer_lag_messages", Help: "Messages pending for a stream consumer.",
	}, []string{"consumer"})
	m.LeasesHeld = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "pandaeye", Name: "leases_held", Help: "Leases held by this process.",
	}, []string{"group"})
	m.LeaseChanges = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pandaeye", Name: "lease_changes_total", Help: "Lease acquisitions, releases and losses.",
	}, []string{"group", "event"})
	m.Signals = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pandaeye", Name: "signals_total", Help: "Signals emitted by check.",
	}, []string{"check"})
	m.Verdicts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pandaeye", Name: "verdicts_total", Help: "Verdicts produced by action.",
	}, []string{"action", "shadow"})
	m.PublishErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pandaeye", Name: "stream_publish_errors_total", Help: "Failed stream publishes by stream.",
	}, []string{"stream"})
	for _, c := range []prometheus.Collector{m.ProbeRTT, m.Batches, m.Events, m.BatchBytes, m.Drops, m.Auth, m.Connections,
		m.ConnectionsClosed, m.ConsumerLag, m.LeasesHeld, m.LeaseChanges, m.Signals, m.Verdicts, m.PublishErrors,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})} {
		f(c)
	}
	return m
}
