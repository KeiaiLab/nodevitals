package nodecompat

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	filefdAllocatedDesc = prometheus.NewDesc(
		"node_filefd_allocated",
		"File descriptor statistics: allocated.",
		nil, nil,
	)
	filefdMaximumDesc = prometheus.NewDesc(
		"node_filefd_maximum",
		"File descriptor statistics: maximum.",
		nil, nil,
	)
)

type fileFDCollector struct {
	procRoot string
}

func newFileFD(procRoot string) subCollector {
	return &fileFDCollector{procRoot: procRoot}
}

func (c *fileFDCollector) Name() string { return "filefd" }

func (c *fileFDCollector) Collect(ch chan<- prometheus.Metric) error {
	path := filepath.Join(c.procRoot, "sys/fs/file-nr")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file-nr: %w", err)
	}

	parts := strings.Fields(string(data))
	if len(parts) < 3 {
		return fmt.Errorf("invalid file-nr format: %q", string(data))
	}

	alloc, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return fmt.Errorf("parse filefd allocated: %w", err)
	}
	max, err := strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return fmt.Errorf("parse filefd maximum: %w", err)
	}

	ch <- prometheus.MustNewConstMetric(filefdAllocatedDesc, prometheus.GaugeValue, alloc)
	ch <- prometheus.MustNewConstMetric(filefdMaximumDesc, prometheus.GaugeValue, max)
	return nil
}
