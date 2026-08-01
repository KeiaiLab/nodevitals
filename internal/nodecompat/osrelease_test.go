package nodecompat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func writeOSRelease(t *testing.T, rootFS, body string) {
	t.Helper()
	dir := filepath.Join(rootFS, "etc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir etc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "os-release"), []byte(body), 0o644); err != nil {
		t.Fatalf("write os-release: %v", err)
	}
}

func TestOSRelease(t *testing.T) {
	rootFS := t.TempDir()
	// Live /etc/os-release from e21, quoting and all.
	writeOSRelease(t, rootFS, `PRETTY_NAME="Ubuntu 24.04.4 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.4 LTS (Noble Numbat)"
VERSION_CODENAME=noble
ID=ubuntu
ID_LIKE=debian
HOME_URL="https://www.ubuntu.com/"
`)

	golden := `# HELP node_os_info A metric with a constant '1' value labeled by build_id, id, id_like, image_id, image_version, name, pretty_name, variant, variant_id, version, version_codename, version_id.
# TYPE node_os_info gauge
node_os_info{build_id="",id="ubuntu",id_like="debian",image_id="",image_version="",name="Ubuntu",pretty_name="Ubuntu 24.04.4 LTS",variant="",variant_id="",version="24.04.4 LTS (Noble Numbat)",version_codename="noble",version_id="24.04"} 1
# HELP node_os_version Metric containing the major.minor part of the OS version.
# TYPE node_os_version gauge
node_os_version{id="ubuntu",id_like="debian",name="Ubuntu"} 24.04
`
	// Restricted to this group's own names — see loadavg_test.go for why.
	if err := testutil.CollectAndCompare(exporterWith(newOSRelease(rootFS)), strings.NewReader(golden),
		"node_os_info", "node_os_version"); err != nil {
		t.Errorf("os-release exposition drifted:\n%v", err)
	}
}

// A VERSION_ID the kernel vendor wrote as a single number ("42") still has to
// produce a parseable node_os_version.
func TestOSReleaseSingleComponentVersion(t *testing.T) {
	rootFS := t.TempDir()
	writeOSRelease(t, rootFS, "NAME=\"Fedora\"\nID=fedora\nVERSION_ID=42\n")

	golden := `# HELP node_os_version Metric containing the major.minor part of the OS version.
# TYPE node_os_version gauge
node_os_version{id="fedora",id_like="",name="Fedora"} 42
`
	if err := testutil.CollectAndCompare(exporterWith(newOSRelease(rootFS)),
		strings.NewReader(golden), "node_os_version"); err != nil {
		t.Errorf("single-component version:\n%v", err)
	}
}

// A distribution that sets SUPPORT_END (e21's does not, which is why this
// family had no golden coverage until now) must produce
// node_os_support_end_timestamp_seconds — --no-collector.os deletes upstream's
// copy outright, so a native gap here would be silent on every such host.
func TestOSReleaseSupportEnd(t *testing.T) {
	rootFS := t.TempDir()
	writeOSRelease(t, rootFS, `PRETTY_NAME="Ubuntu 24.04.4 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.4 LTS (Noble Numbat)"
VERSION_CODENAME=noble
ID=ubuntu
ID_LIKE=debian
SUPPORT_END=2029-04-25
`)

	// time.Parse(time.DateOnly, "2029-04-25").Unix() == 1871769600.
	golden := `# HELP node_os_support_end_timestamp_seconds Metric containing the end-of-life date timestamp of the OS.
# TYPE node_os_support_end_timestamp_seconds gauge
node_os_support_end_timestamp_seconds 1.8717696e+09
`
	if err := testutil.CollectAndCompare(exporterWith(newOSRelease(rootFS)),
		strings.NewReader(golden), "node_os_support_end_timestamp_seconds"); err != nil {
		t.Errorf("support_end exposition drifted:\n%v", err)
	}
}

// A malformed SUPPORT_END must fail the whole group rather than silently
// omit just that one metric — matching upstream's UpdateStruct, which
// computes the parsed date before anything is sent to ch.
func TestOSReleaseSupportEndUnparseableErrors(t *testing.T) {
	rootFS := t.TempDir()
	writeOSRelease(t, rootFS, "NAME=\"Fedora\"\nID=fedora\nVERSION_ID=42\nSUPPORT_END=not-a-date\n")

	ch := make(chan prometheus.Metric, 8)
	if err := newOSRelease(rootFS).Collect(ch); err == nil {
		t.Fatal("want error for an unparseable SUPPORT_END, got nil")
	}
	if len(ch) != 0 {
		t.Fatalf("emitted %d metrics before failing, want 0 (node_os_info must not appear either)", len(ch))
	}
}

// Upstream falls back to /usr/lib/os-release when /etc/os-release is absent.
// Native must too, or an immutable-root distribution that ships only the
// /usr/lib copy loses node_os_info/node_os_version outright once
// --no-collector.os deletes the embedded copy.
func TestOSReleaseUsrLibFallback(t *testing.T) {
	rootFS := t.TempDir()
	dir := filepath.Join(rootFS, "usr", "lib")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir usr/lib: %v", err)
	}
	body := `PRETTY_NAME="Ubuntu 24.04.4 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.4 LTS (Noble Numbat)"
VERSION_CODENAME=noble
ID=ubuntu
ID_LIKE=debian
`
	if err := os.WriteFile(filepath.Join(dir, "os-release"), []byte(body), 0o644); err != nil {
		t.Fatalf("write usr/lib/os-release: %v", err)
	}
	// Deliberately no /etc/os-release in rootFS — this is the fallback path.

	golden := `# HELP node_os_info A metric with a constant '1' value labeled by build_id, id, id_like, image_id, image_version, name, pretty_name, variant, variant_id, version, version_codename, version_id.
# TYPE node_os_info gauge
node_os_info{build_id="",id="ubuntu",id_like="debian",image_id="",image_version="",name="Ubuntu",pretty_name="Ubuntu 24.04.4 LTS",variant="",variant_id="",version="24.04.4 LTS (Noble Numbat)",version_codename="noble",version_id="24.04"} 1
`
	if err := testutil.CollectAndCompare(exporterWith(newOSRelease(rootFS)),
		strings.NewReader(golden), "node_os_info"); err != nil {
		t.Errorf("usr/lib/os-release fallback:\n%v", err)
	}
}
