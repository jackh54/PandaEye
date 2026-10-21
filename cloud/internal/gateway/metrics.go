package gateway

import (
	"errors"

	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

// gwMetrics are the shared collectors plus the gateway's own.
type gwMetrics struct {
	*metrics.Metrics
	backpressure *prometheus.CounterVec // budget: live, replay, bytes, tenant
	outbound     *prometheus.CounterVec // kind
}

func newGWMetrics(m *metrics.Metrics) *gwMetrics {
	g := &gwMetrics{Metrics: m}
	g.backpressure = register(m, prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pandaeye", Name: "gateway_backpressure_seconds_total",
		Help: "Time the gateway stopped reading connections because a budget was exhausted.",
	}, []string{"budget"}))
	g.outbound = register(m, prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pandaeye", Name: "gateway_outbound_total", Help: "Frames delivered to plugins by kind.",
	}, []string{"kind"}))
	return g
}

// register adds c to the registry, reusing an identical collector registered before (several
// gateways in one process share the registry in tests).
func register(m *metrics.Metrics, c *prometheus.CounterVec) *prometheus.CounterVec {
	if err := m.Registry.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(*prometheus.CounterVec); ok {
				return existing
			}
		}
	}
	return c
}
