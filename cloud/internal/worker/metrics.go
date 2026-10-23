package worker

import (
	"errors"

	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/encoding/protowire"
)

// workerMetrics are the worker's own collectors (no tenant or server labels).
type workerMetrics struct {
	Sessions   prometheus.Gauge
	StateBytes prometheus.Gauge
	Players    prometheus.Gauge
	Clips      *prometheus.CounterVec // result: saved, exists, error, queue_full
	Windows    prometheus.Counter
	MLLoaded   prometheus.Gauge     // 1 while an ML model scores windows
	MLPCheat   prometheus.Histogram // P(cheat) of every scored window
}

func newWorkerMetrics(m *metrics.Metrics) *workerMetrics {
	w := &workerMetrics{
		Sessions: register(m, prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "pandaeye", Name: "worker_sessions",
			Help: "Player sessions held by the worker."})),
		StateBytes: register(m, prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "pandaeye", Name: "worker_state_bytes",
			Help: "Accounted worker state bytes (PE_WORKER_MAX_MEMORY budget)."})),
		Players: register(m, prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "pandaeye", Name: "worker_tracked_players",
			Help: "Players whose checks run."})),
		Clips: register(m, prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "pandaeye", Name: "worker_clips_total",
			Help: "Replay clips by result."}, []string{"result"})),
		Windows: register(m, prometheus.NewCounter(prometheus.CounterOpts{Namespace: "pandaeye", Name: "worker_feature_windows_total",
			Help: "Feature windows finalised."})),
		MLLoaded: register(m, prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "pandaeye", Name: "worker_ml_model_loaded",
			Help: "1 when an ML model is loaded and scores feature windows, 0 when PE_ML_MODEL is unset or did not load."})),
		MLPCheat: register(m, prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "pandaeye", Name: "worker_ml_p_cheat",
			Help:    "Model P(cheat) of every scored feature window.",
			Buckets: []float64{.05, .1, .2, .3, .4, .5, .6, .7, .8, .85, .9, .925, .95, .975, .99}})),
	}
	return w
}

// register adds c to the process registry, or returns the collector already registered under the
// same name (several worker services in one process share them).
func register[C prometheus.Collector](m *metrics.Metrics, c C) C {
	if err := m.Registry.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(C); ok {
				return existing
			}
		}
	}
	return c
}

// splitEvents returns the encoded bytes of every StreamBatch.events element (field 5), in order, or
// nil when b cannot be walked.
func splitEvents(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil
		}
		b = b[n:]
		if num == 5 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil
			}
			out = append(out, v)
			b = b[n:]
			continue
		}
		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			return nil
		}
		b = b[n:]
	}
	return out
}
