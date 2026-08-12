package nodecompat

import (
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sys/unix"
)

var unameDesc = prometheus.NewDesc(
	"node_uname_info",
	"Labeled system information as provided by the uname system call.",
	[]string{"domainname", "machine", "nodename", "release", "sysname", "version"},
	nil,
)

type unameCollector struct{}

func newUname() subCollector {
	return &unameCollector{}
}

func (c *unameCollector) Name() string { return "uname" }

func (c *unameCollector) Supersedes() string { return "uname" }

func (c *unameCollector) Collect(ch chan<- prometheus.Metric) error {
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return err
	}

	sysname := charsToString(uts.Sysname[:])
	nodename := charsToString(uts.Nodename[:])
	release := charsToString(uts.Release[:])
	version := charsToString(uts.Version[:])
	machine := charsToString(uts.Machine[:])
	domainname := "(none)"

	ch <- prometheus.MustNewConstMetric(
		unameDesc,
		prometheus.GaugeValue,
		1.0,
		domainname, machine, nodename, release, sysname, version,
	)
	return nil
}

func charsToString(chars []byte) string {
	for i, b := range chars {
		if b == 0 {
			return string(chars[:i])
		}
	}
	return string(chars)
}
