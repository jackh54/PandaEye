package license

import (
	"errors"

	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

// verifyMetrics counts verifications by result (valid, invalid, format, cached_valid, cached_invalid,
// stale, error). A nil *verifyMetrics counts nothing.
type verifyMetrics struct{ c *prometheus.CounterVec }

func newVerifyMetrics(m *metrics.Metrics, mode string) *verifyMetrics {
	if m == nil {
		return nil
	}
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "pandaeye", Name: "license_verifications_total", Help: "Licence verifications by result.",
		ConstLabels: prometheus.Labels{"mode": mode},
	}, []string{"result"})
	if err := m.Registry.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			return nil
		}
		existing, ok := are.ExistingCollector.(*prometheus.CounterVec)
		if !ok {
			return nil
		}
		c = existing
	}
	return &verifyMetrics{c: c}
}

func (v *verifyMetrics) observe(result string) {
	if v != nil {
		v.c.WithLabelValues(result).Inc()
	}
}
