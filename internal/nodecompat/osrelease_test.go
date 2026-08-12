package nodecompat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestOSReleaseCollector(t *testing.T) {
	rootFS := t.TempDir()
	osReleaseContent := `NAME="Ubuntu"
VERSION="22.04.3 LTS (Jammy Jellyfish)"
ID=ubuntu
ID_LIKE=debian
PRETTY_NAME="Ubuntu 22.04.3 LTS"
VERSION_ID="22.04"
VERSION_CODENAME=jammy
`
	etcDir := filepath.Join(rootFS, "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		t.Fatalf("mkdir etc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(etcDir, "os-release"), []byte(osReleaseContent), 0o644); err != nil {
		t.Fatalf("write os-release: %v", err)
	}

	exp := exporterWith(newOSRelease(rootFS))
	expected := `
		# HELP node_os_info A metric with a constant '1' value labeled by build_id, id, id_like, name, pretty_name, version, version_codename, version_id.
		# TYPE node_os_info gauge
		node_os_info{build_id="",id="ubuntu",id_like="debian",name="Ubuntu",pretty_name="Ubuntu 22.04.3 LTS",version="22.04.3 LTS (Jammy Jellyfish)",version_codename="jammy",version_id="22.04"} 1
		# HELP node_os_version Operating system version.
		# TYPE node_os_version gauge
		node_os_version{id="ubuntu",id_like="debian",name="Ubuntu"} 22.04
	`

	if err := testutil.CollectAndCompare(exp, strings.NewReader(expected)); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}
