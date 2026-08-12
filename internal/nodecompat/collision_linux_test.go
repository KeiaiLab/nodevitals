//go:build linux

// 이 검사는 Linux 에서만 의미가 있다. entropy·filefd·stat·vmstat 은 upstream 에
// Linux 전용 구현뿐이라, darwin 에서 돌리면 upstream 쪽 집합이 애초에 비어
// 교집합도 비고 "충돌 없음"으로 통과해버린다 — 실제로는 아무것도 검사하지 않은 채.
package nodecompat_test

import (
	"io"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/KeiaiLab/nodevitals/internal/nodecompat"
	"github.com/KeiaiLab/nodevitals/internal/nodeexporter"
)

// 자체 수집기와 임베드 node_exporter 가 같은 메트릭 이름을 내면 client_golang 이
// 충돌한 family 를 스크레이프에서 빼면서도 200 을 준다. 앞선 단위 테스트는
// "선언과 플래그가 서로 일관적"인지만 보므로, 선언 자체가 틀렸을 때 —
// 예를 들어 procs 가 upstream "stat" 이 아니라 존재하지도 않는 "procs" 를
// 대체한다고 선언했을 때 — 는 잡지 못한다. 실제로 양쪽을 수집해 대조한다.
func TestNativeCollectorsDoNotCollideWithEmbeddedNodeExporter(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	native := names(t, nodecompat.New("/proc", "/sys", "/", quiet))

	// node_exporter 의 collector 들은 init() 에서 전역 kingpin 에 플래그를 등록하고
	// nodeexporter.New 가 그것을 딱 한 번 파싱한다. 한 프로세스에서 구성을 바꿔
	// 두 번 만들 수 없으므로 이 테스트가 유일한 호출자여야 한다.
	ne, err := nodeexporter.New(nodeexporter.Config{
		ProcPath:   "/proc",
		SysPath:    "/sys",
		ExtraFlags: nodecompat.NoCollectorFlags(),
	}, quiet)
	if err != nil {
		t.Fatalf("build embedded node_exporter: %v", err)
	}
	upstream := names(t, ne)

	// 이 단언이 없으면, upstream 수집이 통째로 실패했을 때도 교집합이 비어
	// 통과한다 — 검사한 게 없는데 초록불이 켜지는 바로 그 상태.
	if len(upstream) < 20 {
		t.Fatalf("embedded node_exporter yielded only %d metric families; "+
			"too few to prove anything about collisions", len(upstream))
	}
	if len(native) == 0 {
		t.Fatal("native collectors yielded no metric families")
	}

	for name := range native {
		if upstream[name] {
			t.Errorf("%q is emitted by both the native collector and the embedded "+
				"node_exporter; the collided family gets dropped from every scrape "+
				"while /metrics still answers 200", name)
		}
	}
}

// names 는 collector 를 실제로 수집해 방출된 메트릭 이름을 모은다. 수집 오류는
// 무시한다 — 컨테이너에 없는 하드웨어를 읽는 collector 는 정상적으로 실패하고,
// 여기서 필요한 건 "어떤 이름을 쓰는가"뿐이다.
func names(t *testing.T, c prometheus.Collector) map[string]bool {
	t.Helper()
	reg := prometheus.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("register collector: %v", err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Logf("gather reported errors (expected for absent hardware): %v", err)
	}
	out := make(map[string]bool, len(families))
	for _, f := range families {
		out[f.GetName()] = true
	}
	return out
}
