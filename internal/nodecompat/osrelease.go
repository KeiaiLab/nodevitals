package nodecompat

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	osInfoDesc = prometheus.NewDesc(
		"node_os_info",
		"A metric with a constant '1' value labeled by build_id, id, id_like, name, pretty_name, version, version_codename, version_id.",
		[]string{"build_id", "id", "id_like", "name", "pretty_name", "version", "version_codename", "version_id"},
		nil,
	)
	osVersionDesc = prometheus.NewDesc(
		"node_os_version",
		"Operating system version.",
		[]string{"id", "id_like", "name"},
		nil,
	)
)

type osReleaseCollector struct {
	rootFS string
}

func newOSRelease(rootFS string) subCollector {
	return &osReleaseCollector{rootFS: rootFS}
}

func (c *osReleaseCollector) Name() string { return "osrelease" }

func (c *osReleaseCollector) Collect(ch chan<- prometheus.Metric) error {
	m, err := parseOSRelease(c.rootFS)
	if err != nil {
		return err
	}

	buildID := m["BUILD_ID"]
	id := m["ID"]
	idLike := m["ID_LIKE"]
	name := m["NAME"]
	prettyName := m["PRETTY_NAME"]
	version := m["VERSION"]
	versionCodename := m["VERSION_CODENAME"]
	versionID := m["VERSION_ID"]

	ch <- prometheus.MustNewConstMetric(
		osInfoDesc,
		prometheus.GaugeValue,
		1.0,
		buildID, id, idLike, name, prettyName, version, versionCodename, versionID,
	)

	if versionID != "" {
		if verNum, err := strconv.ParseFloat(versionID, 64); err == nil {
			ch <- prometheus.MustNewConstMetric(
				osVersionDesc,
				prometheus.GaugeValue,
				verNum,
				id, idLike, name,
			)
		}
	}

	return nil
}

func parseOSRelease(rootFS string) (map[string]string, error) {
	paths := []string{
		filepath.Join(rootFS, "etc/os-release"),
		filepath.Join(rootFS, "usr/lib/os-release"),
	}
	m := make(map[string]string)
	var file *os.File
	var err error

	for _, p := range paths {
		file, err = os.Open(p)
		if err == nil {
			break
		}
	}
	if file == nil {
		return m, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := parts[0]
			v := strings.Trim(parts[1], `"'`)
			m[k] = v
		}
	}
	return m, scanner.Err()
}
