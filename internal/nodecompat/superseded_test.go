package nodecompat

import (
	"strings"
	"testing"
)

// Every native sub-collector takes over a metric group that an upstream
// node_exporter collector also owns. If it does not say which one, main.go
// cannot disable that upstream collector, and both register the same metric
// names — client_golang then drops the collided family from the scrape while
// still returning 200, so the loss is silent.
func TestEverySubCollectorDeclaresSupersededUpstreamCollector(t *testing.T) {
	e := New("/proc", "/sys", "/", nil)
	if len(e.subs) == 0 {
		t.Fatal("no sub-collectors registered; this guard would pass vacuously")
	}
	for _, sub := range e.subs {
		if sub.Supersedes() == "" {
			t.Errorf("sub-collector %q declares no superseded upstream collector: "+
				"the embedded node_exporter will keep emitting the same metrics and duplicate it",
				sub.Name())
		}
	}
}

// The flags are what actually reach node_exporter's kingpin parser, which
// rejects anything outside the --collector.* / --no-collector.* namespace.
func TestNoCollectorFlagsCoverEverySupersededCollector(t *testing.T) {
	flags := NoCollectorFlags()
	superseded := SupersededCollectors()
	// Without this both lists can be empty and every assertion below passes
	// vacuously — the exact shape of the bug this guards against.
	if len(superseded) == 0 {
		t.Fatal("SupersededCollectors() is empty; no upstream collector would be disabled")
	}
	if len(flags) != len(superseded) {
		t.Fatalf("got %d flags for %d superseded collectors: %v", len(flags), len(superseded), flags)
	}
	for _, name := range superseded {
		want := "--no-collector." + name
		if !contains(flags, want) {
			t.Errorf("missing %q; upstream %q stays enabled and duplicates the native collector", want, name)
		}
	}
	for _, f := range flags {
		if !strings.HasPrefix(f, "--no-collector.") {
			t.Errorf("flag %q is outside the --no-collector.* namespace and node_exporter will refuse to start", f)
		}
	}
}

// procs supersedes upstream "stat" and osrelease supersedes upstream "os" —
// neither name matches the sub-collector's own Name(). Deriving the flags from
// Name() would leave both upstream collectors enabled.
func TestSupersededNamesAreUpstreamNamesNotLocalNames(t *testing.T) {
	e := New("/proc", "/sys", "/", nil)
	want := map[string]string{
		"osrelease": "os",
	}
	for _, sub := range e.subs {
		if w, ok := want[sub.Name()]; ok && sub.Supersedes() != w {
			t.Errorf("sub-collector %q supersedes %q, want upstream name %q",
				sub.Name(), sub.Supersedes(), w)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
