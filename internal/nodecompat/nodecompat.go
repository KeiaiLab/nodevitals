// Package nodecompat implements native node_* collectors for nodevitals,
// replacing the embedded node_exporter for core /proc-backed metric groups.
package nodecompat

import (
	"log/slog"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// subCollector is the internal interface for individual metric group collectors.
type subCollector interface {
	Name() string
	Collect(ch chan<- prometheus.Metric) error
}

// Exporter collects native node_* metrics and satisfies prometheus.Collector.
type Exporter struct {
	subs []subCollector
	log  *slog.Logger

	mu     sync.Mutex
	logged map[string]bool
}

// New returns a new native nodecompat Exporter configured with procRoot, sysRoot, and rootFS.
func New(procRoot, sysRoot, rootFS string, log *slog.Logger) *Exporter {
	if log == nil {
		log = slog.Default()
	}
	return &Exporter{
		subs: []subCollector{
			newLoadAvg(procRoot),
			newFileFD(procRoot),
			newEntropy(procRoot),
			newProcs(procRoot),
			newVMStat(procRoot),
			newUname(),
			newOSRelease(rootFS),
		},
		log:    log,
		logged: make(map[string]bool),
	}
}

// Describe satisfies prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	// Unchecked collector: Describe emits nothing, allowing dynamically created metrics.
}

// Collect satisfies prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	for _, sub := range e.subs {
		if err := sub.Collect(ch); err != nil {
			e.logOnce(sub.Name(), err)
		}
	}
}

func (e *Exporter) logOnce(name string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.logged[name] {
		e.log.Warn("sub-collector failed", "collector", name, "err", err)
		e.logged[name] = true
	}
}
