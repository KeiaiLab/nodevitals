package nodecompat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
