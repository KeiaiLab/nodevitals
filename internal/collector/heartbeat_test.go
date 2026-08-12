package collector

import (
	"context"
	"testing"
)

// nodevitals_build_info 는 배포 검증의 유일한 자기신고 수단이다. 버전이 주입되지
// 않았을 때 그럴듯한 릴리스 번호를 채우면, 실제로 도는 이미지가 무엇인지 물어볼
// 곳이 없어진다 — 0.9.0 이미지가 version="0.8.5" 를 내던 것이 정확히 그 상태였다.
func TestHeartbeatReportsUnknownRatherThanInventingAVersion(t *testing.T) {
	c := NewHeartbeat("node-1", "")

	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, s := range samples {
		if s.Metric != "nodevitals_build_info" {
			continue
		}
		if got := s.Labels["version"]; got != "unknown" {
			t.Errorf("version=%q with nothing injected; a build that does not know its "+
				"own version must say so rather than name a release it may not be", got)
		}
		return
	}
	t.Fatal("nodevitals_build_info not emitted")
}

func TestHeartbeatCollector(t *testing.T) {
	c := NewHeartbeat("node-1", "0.8.5")
	if c.Name() != "heartbeat" {
		t.Fatalf("unexpected name: %s", c.Name())
	}

	samples, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(samples) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(samples))
	}

	upFound := false
	buildInfoFound := false
	for _, s := range samples {
		if s.Metric == "nodevitals_up" && s.Value == 1.0 {
			upFound = true
		}
		if s.Metric == "nodevitals_build_info" && s.Labels["version"] == "0.8.5" {
			buildInfoFound = true
		}
	}

	if !upFound {
		t.Errorf("nodevitals_up missing or invalid")
	}
	if !buildInfoFound {
		t.Errorf("nodevitals_build_info missing or invalid")
	}
}
