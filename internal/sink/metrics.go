package sink

import (
	"net/http"
	"sync"

	"github.com/KeiaiLab/nodevitals/internal/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics exposes the latest sample snapshot as Prometheus gauges. It implements
// prometheus.Collector, emitting const metrics on scrape from the held snapshot.
type Metrics struct {
	mu       sync.RWMutex
	snapshot []model.Sample
	reg      *prometheus.Registry
	dropped  *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nodevitals_delivery_dropped_total",
			Help: "events dropped after exhausting webhook delivery retries (silent-loss signal)",
		}, []string{"sink"}),
	}
	m.reg.MustRegister(m, m.dropped)
	return m
}

// RecordDropped increments the drop counter for a sink by n events. Called when
// DeliverWithRetry exhausts its retries and the batch is lost, so operators can
// alert on otherwise-silent delivery loss.
func (m *Metrics) RecordDropped(sink string, n int) {
	if n > 0 {
		m.dropped.WithLabelValues(sink).Add(float64(n))
	}
}

// Update replaces the exposed snapshot atomically.
func (m *Metrics) Update(samples []model.Sample) {
	m.mu.Lock()
	m.snapshot = samples
	m.mu.Unlock()
}

func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(m, ch)
}

func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.snapshot {
		vt := prometheus.GaugeValue
		if s.Kind == model.KindCounter {
			vt = prometheus.CounterValue
		}
		desc := prometheus.NewDesc(
			"nodevitals_hw_"+s.Metric,
			"nodevitals hardware metric "+s.Metric,
			[]string{"node", "tier", "device"}, nil,
		)
		ch <- prometheus.MustNewConstMetric(desc, vt, s.Value, s.Node, s.Tier, s.Device)
	}
}

// Handler returns the /metrics HTTP handler.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}
