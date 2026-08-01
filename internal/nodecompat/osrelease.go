package nodecompat

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// osInfoLabels is the label set node_exporter emits, in the order its HELP
// text lists them. Fields absent from a given distribution's os-release come
// out empty, which is what upstream does too — an empty label is dropped at
// ingestion, so the stored series is identical.
var osInfoLabels = []string{
	"build_id", "id", "id_like", "image_id", "image_version", "name",
	"pretty_name", "variant", "variant_id", "version", "version_codename", "version_id",
}

var (
	osInfoDesc = prometheus.NewDesc(
		"node_os_info",
		"A metric with a constant '1' value labeled by build_id, id, id_like, image_id, image_version, name, pretty_name, variant, variant_id, version, version_codename, version_id.",
		osInfoLabels, nil)
	osVersionDesc = prometheus.NewDesc(
		"node_os_version",
		"Metric containing the major.minor part of the OS version.",
		[]string{"id", "id_like", "name"}, nil)
	osSupportEndDesc = prometheus.NewDesc(
		"node_os_support_end_timestamp_seconds",
		"Metric containing the end-of-life date timestamp of the OS.",
		nil, nil)
)

type osRelease struct{ rootFS string }

func newOSRelease(rootFS string) subCollector { return &osRelease{rootFS: rootFS} }

func (o *osRelease) Name() string { return "os" }

func (o *osRelease) Collect(ch chan<- prometheus.Metric) error {
	kv, err := parseOSRelease(filepath.Join(o.rootFS, "etc", "os-release"))
	if errors.Is(err, os.ErrNotExist) {
		// Upstream falls back to /usr/lib/os-release when /etc/os-release is
		// missing — immutable-root and initramfs-built distributions commonly
		// ship only the /usr/lib copy and reserve /etc for a local override.
		kv, err = parseOSRelease(filepath.Join(o.rootFS, "usr", "lib", "os-release"))
	}
	if err != nil {
		return err
	}

	// Parsed before anything is sent to ch: a SUPPORT_END that fails to parse
	// must cost the whole group, the same all-or-nothing rule upstream's
	// UpdateStruct enforces (collector/os_release.go) — node_os_info without a
	// stale/wrong node_os_support_end_timestamp_seconds is a worse outcome
	// than neither.
	var supportEndUnix float64
	haveSupportEnd := false
	if raw := kv["SUPPORT_END"]; raw != "" {
		t, perr := time.Parse(time.DateOnly, raw)
		if perr != nil {
			return fmt.Errorf("os-release SUPPORT_END=%q: %w", raw, perr)
		}
		supportEndUnix = float64(t.Unix())
		haveSupportEnd = true
	}

	values := make([]string, len(osInfoLabels))
	for i, l := range osInfoLabels {
		values[i] = kv[strings.ToUpper(l)]
	}
	ch <- prometheus.MustNewConstMetric(osInfoDesc, prometheus.GaugeValue, 1, values...)

	// node_os_version carries only major.minor as a number, so a VERSION_ID the
	// vendor wrote as "24.04.4" or "42" must both parse.
	if v, ok := majorMinor(kv["VERSION_ID"]); ok {
		ch <- prometheus.MustNewConstMetric(osVersionDesc, prometheus.GaugeValue, v,
			kv["ID"], kv["ID_LIKE"], kv["NAME"])
	}

	// Most distributions never set SUPPORT_END — e21's does not, which is why
	// this family had no golden coverage until now. The metric is omitted
	// entirely rather than emitted as 0, matching upstream.
	if haveSupportEnd {
		ch <- prometheus.MustNewConstMetric(osSupportEndDesc, prometheus.GaugeValue, supportEndUnix)
	}
	return nil
}

// parseOSRelease reads the shell-fragment format: KEY=value or KEY="value",
// blank lines and # comments skipped.
func parseOSRelease(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	kv := map[string]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return kv, s.Err()
}

// majorMinor turns "24.04.4" into 24.04 and "42" into 42. ok is false when the
// field is absent or unparseable, and the caller then omits the metric rather
// than reporting version 0.
func majorMinor(version string) (float64, bool) {
	if version == "" {
		return 0, false
	}
	parts := strings.SplitN(version, ".", 3)
	joined := parts[0]
	if len(parts) > 1 {
		joined += "." + parts[1]
	}
	v, err := strconv.ParseFloat(joined, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
