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
	// Supersedes is the upstream node_exporter collector name whose metrics
	// this one takes over. It is a compile-time obligation on purpose: a new
	// sub-collector that does not answer it cannot be added to the set, and an
	// upstream collector left enabled alongside its native replacement makes
	// both register the same metric names.
	Supersedes() string
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
			newVMStat(procRoot),
			// procs(node_procs_running/blocked)는 의도적으로 없다. upstream 의
			// "stat" collector 가 같은 /proc/stat 에서 그 둘에 더해
			// boot_time_seconds·context_switches_total·forks_total·intr_total 까지
			// 내므로, 둘만 내면서 stat 을 끄면 나머지 넷이 통째로 사라진다
			// (2026-08-12 카나리 실측). parity 테스트가 이 조건을 강제한다.
			newUname(),
			newOSRelease(rootFS),
		},
		log:    log,
		logged: make(map[string]bool),
	}
}

// SupersededCollectors returns the upstream node_exporter collector names that
// this package's native collectors replace. It is derived from the set itself,
// not from a second hand-kept list: the two drifting apart is precisely how a
// native collector ends up running alongside the upstream one it replaced.
func SupersededCollectors() []string {
	e := New("", "", "", nil)
	seen := make(map[string]bool, len(e.subs))
	names := make([]string, 0, len(e.subs))
	for _, sub := range e.subs {
		n := sub.Supersedes()
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	return names
}

// NoCollectorFlags returns the node_exporter flags that disable every upstream
// collector superseded by this package. Pass them to nodeexporter.Config's
// ExtraFlags whenever the native collectors are enabled.
func NoCollectorFlags() []string {
	superseded := SupersededCollectors()
	flags := make([]string, 0, len(superseded))
	for _, n := range superseded {
		flags = append(flags, "--no-collector."+n)
	}
	return flags
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
