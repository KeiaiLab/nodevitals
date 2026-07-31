// Package nodecompat serves the upstream node_exporter metric surface from
// nodevitals' own readers, so the node_* families come from this codebase
// rather than from a vendored collector set — the /proc sibling of
// internal/dcgmcompat and internal/smartctlcompat.
//
// Each metric group is one subCollector reading one file under an injected
// root. Descriptors are copied byte-for-byte from a live node_exporter 1.12.1
// scrape: name, HELP, type, and label names are the compatibility contract,
// and the golden tests beside each file are what holds them to it.
//
// A group that lands here MUST be disabled on the embedded side with
// --no-collector.<name>. Two collectors emitting one metric name make the
// registry reject the whole scrape, so the failure is total rather than
// partial.
package nodecompat

import (
	"log/slog"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// subCollector is one /proc- or /sys-backed metric group. Collect writes its
// metrics to ch and returns an error only for conditions the operator can act
// on — a missing file, an unparseable line. "This machine has no such
// hardware" is not an error; it is an empty result.
type subCollector interface {
	Name() string
	Collect(ch chan<- prometheus.Metric) error
}

// Exporter implements prometheus.Collector over the registered sub-collectors.
type Exporter struct {
	log  *slog.Logger
	subs []subCollector

	// warned keeps a broken sub-collector to a single log line rather than one
	// per scrape. An unreadable /proc file is a deployment fact — it does not
	// change until the pod is restarted with different mounts.
	warned sync.Map
}

// New wires the sub-collectors this package serves. procRoot is the host /proc
// mount and rootFS the host root mount; both are injected so tests can point
// at a fixture directory.
func New(procRoot, rootFS string, log *slog.Logger) *Exporter {
	return &Exporter{
		log: log,
		subs: []subCollector{
			newLoadAvg(procRoot),
			newFileFD(procRoot),
			newEntropy(procRoot),
			newProcs(procRoot),
			newVMStat(procRoot),
			newUname(realUname),
			newOSRelease(rootFS),
		},
	}
}

func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(e, ch)
}

// Collect runs every sub-collector. One failure is logged once and skipped so
// a single unreadable file cannot cost the operator every other family —
// the same per-collector isolation internal/collector.Registry.CollectAll uses.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	for _, s := range e.subs {
		if err := s.Collect(ch); err != nil {
			if _, seen := e.warned.LoadOrStore(s.Name(), true); !seen {
				e.log.Warn("nodecompat collector failed", "collector", s.Name(), "err", err)
			}
		}
	}
}
