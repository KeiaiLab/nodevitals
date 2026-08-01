package nodecompat

import "sort"

// UpstreamCollectors maps each native group this package serves to the
// upstream node_exporter collector name that owns the same metric names. The
// mapping is not always symmetric with the group's own name: node_exporter's
// "stat" collector (collector/stat_linux.go, registerCollector("stat",
// defaultEnabled, ...)) owns node_procs_running/node_procs_blocked, not a
// "processes" collector — that one (collector/processes_linux.go,
// registerCollector("processes", defaultDisabled, ...)) owns entirely
// different names (node_processes_threads/_state/_pids/_max_processes). That
// mismatch is exactly why this package has no group for those two metrics:
// disabling "stat" to avoid the collision would also delete node_intr_total,
// node_context_switches_total, node_forks_total, node_boot_time_seconds and
// node_softirqs_total — five families this package does not implement. A
// future phase adding a group MUST verify its upstream owner against that
// group's registerCollector() call in the vendored source, not assume the
// names match.
var UpstreamCollectors = map[string]string{
	"loadavg": "loadavg",
	"filefd":  "filefd",
	"entropy": "entropy",
	"vmstat":  "vmstat",
	"uname":   "uname",
	"os":      "os",
}

// ConflictingUpstreamCollectors reports, sorted, which native groups in
// UpstreamCollectors still have their upstream twin present in
// enabledUpstream — the collector names node_exporter reports live via
// nodeexporter.Enabled. A non-empty result means enabling NativeCollectors
// would make two collectors emit the same node_* metric names into the same
// scrape, which the Prometheus registry rejects outright rather than
// dropping just the duplicate. This is the check that survives a
// hand-written ConfigMap: the chart always pairs nativeCollectors with the
// matching --no-collector.* flags, but nothing stops a ConfigMap authored by
// hand (or by an older chart release) from setting one without the other.
func ConflictingUpstreamCollectors(enabledUpstream []string) []string {
	enabled := make(map[string]bool, len(enabledUpstream))
	for _, n := range enabledUpstream {
		enabled[n] = true
	}
	var conflicts []string
	for group, upstream := range UpstreamCollectors {
		if enabled[upstream] {
			conflicts = append(conflicts, group)
		}
	}
	sort.Strings(conflicts)
	return conflicts
}
