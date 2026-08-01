package nodecompat

import (
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestUnameInfo(t *testing.T) {
	// Live values from e21.
	fake := func() (unameInfo, error) {
		return unameInfo{
			Sysname:    "Linux",
			Nodename:   "e21",
			Release:    "6.17.0-22-generic",
			Version:    "#22~24.04.1-Ubuntu SMP PREEMPT_DYNAMIC Thu Mar 26 15:25:54 UTC 2",
			Machine:    "x86_64",
			Domainname: "(none)",
		}, nil
	}

	golden := `# HELP node_uname_info Labeled system information as provided by the uname system call.
# TYPE node_uname_info gauge
node_uname_info{domainname="(none)",machine="x86_64",nodename="e21",release="6.17.0-22-generic",sysname="Linux",version="#22~24.04.1-Ubuntu SMP PREEMPT_DYNAMIC Thu Mar 26 15:25:54 UTC 2"} 1
`
	// Restricted to this group's own name — see loadavg_test.go for why.
	if err := testutil.CollectAndCompare(exporterWith(newUname(fake)), strings.NewReader(golden),
		"node_uname_info"); err != nil {
		t.Errorf("uname exposition drifted:\n%v", err)
	}
}

func TestUnameSyscallFailurePropagates(t *testing.T) {
	fail := func() (unameInfo, error) { return unameInfo{}, errors.New("EFAULT") }

	if err := newUname(fail).Collect(make(chan prometheus.Metric, 8)); err == nil {
		t.Fatal("want error when the syscall fails, got nil")
	}
}
