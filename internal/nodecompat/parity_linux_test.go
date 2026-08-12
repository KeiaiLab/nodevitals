//go:build linux

// 이 검사는 Linux 에서만 의미가 있다. entropy·filefd·stat·vmstat 은 upstream 에
// Linux 전용 구현뿐이라, darwin 에서는 비교 대상 자체가 비어 통과해버린다.
package nodecompat_test

import (
	"io"
	"log/slog"
	"regexp"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	necollector "github.com/prometheus/node_exporter/collector"

	"github.com/KeiaiLab/nodevitals/internal/nodecompat"
	"github.com/KeiaiLab/nodevitals/internal/nodeexporter"
)

var fqName = regexp.MustCompile(`fqName: "([^"]+)"`)

// 자체 수집기가 어떤 upstream collector 를 대체한다고 선언하면 그 collector 는
// 통째로 꺼진다. 따라서 두 집합이 정확히 같아야 한다.
//
//	upstream 에만 있는 이름 → 그 메트릭은 아무데서도 나오지 않는다(손실)
//	native 에만 있는 이름   → 끄지 못한 다른 collector 와 충돌한다(중복)
//
// 손실 쪽이 특히 잡기 어렵다. 이름만 보고는 어느 collector 소관인지 알 수 없기
// 때문이다 — upstream 의 "stat" 은 node_procs_running 뿐 아니라
// node_boot_time_seconds·node_context_switches_total·node_forks_total·
// node_intr_total 도 낸다. 실측으로 대조하지 않으면 넷이 조용히 사라진다.
func TestNativeCollectorsMatchTheUpstreamCollectorsTheyReplace(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	// 차단 플래그 없이 만든다 — 대체 대상 collector 를 개별로 수집해야 하므로.
	// node_exporter 의 collector 들은 init() 에서 전역 kingpin 에 플래그를 등록하고
	// nodeexporter.New 가 그것을 딱 한 번 파싱하므로, 이 테스트가 이 패키지의
	// 유일한 호출자여야 한다.
	c, err := nodeexporter.New(nodeexporter.Config{ProcPath: "/proc", SysPath: "/sys"}, quiet)
	if err != nil {
		t.Fatalf("build embedded node_exporter: %v", err)
	}
	node, ok := c.(*necollector.NodeCollector)
	if !ok {
		t.Fatalf("embedded collector is %T, not a *NodeCollector", c)
	}

	upstream := map[string]bool{}
	for _, name := range nodecompat.SupersededCollectors() {
		sub, ok := node.Collectors[name]
		if !ok {
			t.Errorf("nodecompat claims to supersede upstream collector %q, which is not enabled "+
				"(a typo here means the upstream collector keeps running and duplicates the native one)", name)
			continue
		}
		for n := range namesFromUpdate(t, sub) {
			upstream[n] = true
		}
	}
	if len(upstream) == 0 {
		t.Fatal("superseded upstream collectors emitted nothing; this comparison would pass vacuously")
	}

	native := namesFromCollector(t, nodecompat.New("/proc", "/sys", "/", quiet))

	for n := range upstream {
		if !native[n] {
			t.Errorf("%q is emitted by a superseded upstream collector but not by the native one; "+
				"disabling that collector deletes the metric outright", n)
		}
	}
	for n := range native {
		if !upstream[n] {
			t.Errorf("%q is emitted natively but by none of the superseded upstream collectors; "+
				"whichever upstream collector owns it is still enabled and will collide", n)
		}
	}
}

// namesFromUpdate collects one embedded collector on its own, which is the only
// way to learn which metrics that particular collector owns.
func namesFromUpdate(t *testing.T, c necollector.Collector) map[string]bool {
	t.Helper()
	ch := make(chan prometheus.Metric, 4096)
	if err := c.Update(ch); err != nil {
		// 컨테이너에 없는 하드웨어를 읽는 collector 는 정상적으로 실패한다.
		t.Logf("collector update reported (may be expected): %v", err)
	}
	close(ch)
	out := map[string]bool{}
	for m := range ch {
		if g := fqName.FindStringSubmatch(m.Desc().String()); g != nil {
			out[g[1]] = true
		}
	}
	return out
}

func namesFromCollector(t *testing.T, c prometheus.Collector) map[string]bool {
	t.Helper()
	reg := prometheus.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("register collector: %v", err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Logf("gather reported: %v", err)
	}
	out := map[string]bool{}
	for _, f := range families {
		out[f.GetName()] = true
	}
	return out
}
