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
	fileFDAllocatedDesc = prometheus.NewDesc(
		"node_filefd_allocated", "File descriptor statistics: allocated.", nil, nil)
	fileFDMaximumDesc = prometheus.NewDesc(
		"node_filefd_maximum", "File descriptor statistics: maximum.", nil, nil)
)

type fileFD struct{ procRoot string }

func newFileFD(procRoot string) subCollector { return &fileFD{procRoot: procRoot} }

func (f *fileFD) Name() string { return "filefd" }

// Collect reads /proc/sys/fs/file-nr, whose three fields are allocated, unused
// and maximum. The middle field has been hardwired to 0 since Linux 2.6, so
// only the outer two carry information.
func (f *fileFD) Collect(ch chan<- prometheus.Metric) error {
	b, err := os.ReadFile(filepath.Join(f.procRoot, "sys", "fs", "file-nr"))
	if err != nil {
		return err
	}
	fields := strings.Fields(string(b))
	if len(fields) != 3 {
		return fmt.Errorf("file-nr: want 3 fields, got %d", len(fields))
	}
	// Parse all values before emitting any, so a bad field doesn't leave the
	// channel with a partial set.
	var allocated, maximum float64
	var parseErr error

	allocated, parseErr = strconv.ParseFloat(fields[0], 64)
	if parseErr != nil {
		return fmt.Errorf("file-nr allocated field (%q): %w", fields[0], parseErr)
	}

	maximum, parseErr = strconv.ParseFloat(fields[2], 64)
	if parseErr != nil {
		return fmt.Errorf("file-nr maximum field (%q): %w", fields[2], parseErr)
	}

	// All parsed successfully; now emit atomically.
	ch <- prometheus.MustNewConstMetric(fileFDAllocatedDesc, prometheus.GaugeValue, allocated)
	ch <- prometheus.MustNewConstMetric(fileFDMaximumDesc, prometheus.GaugeValue, maximum)
	return nil
}
