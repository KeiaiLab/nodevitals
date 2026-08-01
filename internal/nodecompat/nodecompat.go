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
	"time"

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

// scrapeDurationDesc and scrapeSuccessDesc mirror node_exporter's own
// per-collector health series byte-for-byte (name, HELP, type — see
// scrapeDurationDesc/scrapeSuccessDesc in the vendored collector/collector.go).
//
// They must NOT be declared unconditionally. prometheus.Registry rejects a
// second Collector that declares a descriptor with this fqName and these
// (empty) constLabels — Desc.id is a hash of fqName+constLabels alone (see
// Registry.Register in client_golang/prometheus/registry.go), so it collides
// no matter what values the "collector" variable label takes at Collect
// time. "The label values are disjoint" does not help: the six native
// groups are always --no-collector'd on the embedded side, so the two sides
// never emit the same {collector="..."} series, but the registry never gets
// that far — it rejects the whole scrape at Describe time, before any value
// exists. internal/nodeexporter's embedded set (vendored
// NodeCollector.Describe, collector/collector.go) declares exactly this
// fqName+constLabels pair whenever it is registered at all, for every
// collector it runs. So this package's copy is gated by
// Exporter.emitScrapeHealth, which callers MUST keep false while
// internal/nodeexporter is also registered in the process (see
// cmd/nodevitals/main.go). Net effect: the six groups this package serves
// currently have NO scrape-health series of their own for as long as the
// embedded set runs alongside them — a known, documented gap, not a silent
// one. It closes once the embedding is removed entirely, this migration's
// final phase.
var (
	scrapeDurationDesc = prometheus.NewDesc(
		"node_scrape_collector_duration_seconds",
		"node_exporter: Duration of a collector scrape.",
		[]string{"collector"}, nil)
	scrapeSuccessDesc = prometheus.NewDesc(
		"node_scrape_collector_success",
		"node_exporter: Whether a collector succeeded.",
		[]string{"collector"}, nil)
)

// Exporter implements prometheus.Collector over the registered sub-collectors.
type Exporter struct {
	log  *slog.Logger
	subs []subCollector

	// emitScrapeHealth gates the node_scrape_collector_success/duration pair
	// (see scrapeSuccessDesc/scrapeDurationDesc above). Must be false
	// whenever internal/nodeexporter's embedded collector set is also
	// registered in this process — registering both is a Desc-identity
	// collision, not a harmless duplicate.
	emitScrapeHealth bool

	// warned keeps a broken sub-collector to a single log line rather than one
	// per scrape. An unreadable /proc file is a deployment fact — it does not
	// change until the pod is restarted with different mounts.
	warned sync.Map
}

// New wires the sub-collectors this package serves. procRoot is the host /proc
// mount and rootFS the host root mount; both are injected so tests can point
// at a fixture directory. emitScrapeHealth must be false whenever
// internal/nodeexporter's embedded collector set is also registered in this
// process — see scrapeSuccessDesc/scrapeDurationDesc for why.
func New(procRoot, rootFS string, emitScrapeHealth bool, log *slog.Logger) *Exporter {
	if rootFS == "" {
		// Upstream defaults --path.rootfs to "/". Without this, a deployment
		// running with mountRootFS=false (RootFSPath left empty) makes
		// osrelease.go build the relative path "etc/os-release", resolved
		// against this process's working directory instead of the host root.
		rootFS = "/"
	}
	return &Exporter{
		log:              log,
		emitScrapeHealth: emitScrapeHealth,
		subs: []subCollector{
			newLoadAvg(procRoot),
			newFileFD(procRoot),
			newEntropy(procRoot),
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
// the same per-collector isolation internal/collector.Registry.CollectAll
// uses. When emitScrapeHealth is set, each sub-collector also gets a
// node_scrape_collector_success/duration pair, the same per-collector health
// signal node_exporter's own execute() emits (collector/collector.go) — see
// scrapeSuccessDesc/scrapeDurationDesc above for why that pair is gated
// rather than unconditional, and for the gap it currently leaves open while
// internal/nodeexporter is also registered.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	for _, s := range e.subs {
		begin := time.Now()
		err := s.Collect(ch)
		duration := time.Since(begin)

		success := 1.0
		if err != nil {
			success = 0
			if _, seen := e.warned.LoadOrStore(s.Name(), true); !seen {
				e.log.Warn("nodecompat collector failed", "collector", s.Name(), "err", err)
			}
		}
		if e.emitScrapeHealth {
			ch <- prometheus.MustNewConstMetric(scrapeDurationDesc, prometheus.GaugeValue, duration.Seconds(), s.Name())
			ch <- prometheus.MustNewConstMetric(scrapeSuccessDesc, prometheus.GaugeValue, success, s.Name())
		}
	}
}
