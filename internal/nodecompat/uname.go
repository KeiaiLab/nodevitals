package nodecompat

import "github.com/prometheus/client_golang/prometheus"

var unameDesc = prometheus.NewDesc(
	"node_uname_info",
	"Labeled system information as provided by the uname system call.",
	[]string{"sysname", "release", "version", "machine", "nodename", "domainname"},
	nil)

// unameInfo is the subset of struct utsname the metric carries.
type unameInfo struct {
	Sysname, Nodename, Release, Version, Machine, Domainname string
}

// unameFunc is the seam over uname(2). Unlike every other source in this
// package the syscall cannot be pointed at a fixture directory, so tests
// inject a fake instead of a root path.
type unameFunc func() (unameInfo, error)

type uname struct{ get unameFunc }

func newUname(get unameFunc) subCollector { return &uname{get: get} }

func (u *uname) Name() string { return "uname" }

func (u *uname) Collect(ch chan<- prometheus.Metric) error {
	info, err := u.get()
	if err != nil {
		return err
	}
	ch <- prometheus.MustNewConstMetric(unameDesc, prometheus.GaugeValue, 1,
		info.Sysname, info.Release, info.Version, info.Machine, info.Nodename, info.Domainname)
	return nil
}

// nulString trims a NUL-padded fixed byte array (as struct utsname fields
// are) at the first NUL rather than converting the whole backing array.
func nulString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
