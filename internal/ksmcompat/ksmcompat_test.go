package ksmcompat

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const nodeJSON = `{
  "metadata": {"name": "e999"},
  "status": {
    "nodeInfo": {
      "kernelVersion": "6.17.0-test",
      "osImage": "Ubuntu Test 24.04",
      "containerRuntimeVersion": "containerd://9.9.9",
      "kubeProxyVersion": "v1.36.2+test"
    },
    "conditions": [
      {"type": "Ready", "status": "True"},
      {"type": "MemoryPressure", "status": "False"}
    ],
    "capacity":    {"cpu": "7",  "memory": "12345Ki", "pods": "110"},
    "allocatable": {"cpu": "6",  "memory": "12000Ki", "pods": "110"}
  }
}`

const podsJSON = `{"items": [
  {
    "metadata": {"name": "p1", "namespace": "ns1",
      "ownerReferences": [{"kind": "DaemonSet", "name": "ds1"}]},
    "status": {"phase": "Running", "hostIP": "10.31.10.99", "podIP": "10.31.10.99",
      "containerStatuses": [{"name": "c1", "ready": true, "restartCount": 3}]}
  }
]}`

// fakeAPI stands in for the Kubernetes API server. It records what was asked
// for, so a test can assert on the request as well as the response.
type fakeAPI struct {
	*httptest.Server
	podQuery string
	nodePath string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/nodes/"):
			f.nodePath = r.URL.Path
			fmt.Fprint(w, nodeJSON)
		case r.URL.Path == "/api/v1/pods":
			f.podQuery = r.URL.RawQuery
			fmt.Fprint(w, podsJSON)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func gather(t *testing.T, e *Exporter) map[string][]*dto.Metric {
	t.Helper()
	reg := prometheus.NewRegistry()
	if err := reg.Register(e); err != nil {
		t.Fatalf("register: %v", err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Logf("gather reported: %v", err)
	}
	out := map[string][]*dto.Metric{}
	for _, f := range families {
		out[f.GetName()] = f.GetMetric()
	}
	return out
}

func newTestExporter(t *testing.T, apiURL string) *Exporter {
	t.Helper()
	e, err := New(Config{
		Node:   "e999",
		Mode:   "node",
		APIURL: apiURL,
		Token:  "test-token",
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func labelOf(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

// 노드 값은 API 가 말한 것이어야 한다. 상수로 채우면 모든 노드가 같은 용량을
// 신고하고, 용량 기반 알림과 대시보드가 조용히 틀린 기준으로 돈다.
func TestNodeMetricsComeFromTheAPI(t *testing.T) {
	api := newFakeAPI(t)
	got := gather(t, newTestExporter(t, api.URL))

	caps := got["kube_node_status_capacity"]
	if len(caps) == 0 {
		t.Fatal("kube_node_status_capacity not emitted")
	}
	var sawCPU bool
	for _, m := range caps {
		if labelOf(m, "resource") == "cpu" {
			sawCPU = true
			if v := m.GetGauge().GetValue(); v != 7 {
				t.Errorf("cpu capacity = %v, want 7 (the value the API reported)", v)
			}
			if u := labelOf(m, "unit"); u != "core" {
				t.Errorf("cpu unit = %q, want \"core\"", u)
			}
		}
		if labelOf(m, "resource") == "memory" {
			if v := m.GetGauge().GetValue(); v != 12345*1024 {
				t.Errorf("memory capacity = %v, want %v", v, 12345*1024)
			}
		}
	}
	if !sawCPU {
		t.Error("no cpu capacity series")
	}

	info := got["kube_node_info"]
	if len(info) != 1 {
		t.Fatalf("kube_node_info: got %d series, want 1", len(info))
	}
	if k := labelOf(info[0], "kernel_version"); k != "6.17.0-test" {
		t.Errorf("kernel_version = %q, want the value from the API", k)
	}
	if r := labelOf(info[0], "container_runtime_version"); r != "containerd://9.9.9" {
		t.Errorf("container_runtime_version = %q, want the value from the API", r)
	}
}

func TestNodeConditionsComeFromTheAPI(t *testing.T) {
	api := newFakeAPI(t)
	got := gather(t, newTestExporter(t, api.URL))

	conds := got["kube_node_status_condition"]
	if len(conds) == 0 {
		t.Fatal("kube_node_status_condition not emitted")
	}
	// KSM 은 condition 마다 true/false/unknown 세 시리즈를 내고 해당하는 하나만 1 이다.
	found := map[string]float64{}
	for _, m := range conds {
		if labelOf(m, "condition") == "Ready" {
			found[labelOf(m, "status")] = m.GetGauge().GetValue()
		}
	}
	if found["true"] != 1 || found["false"] != 0 || found["unknown"] != 0 {
		t.Errorf("Ready condition series = %v, want true=1 false=0 unknown=0", found)
	}
}

func TestPodMetricsComeFromTheAPI(t *testing.T) {
	api := newFakeAPI(t)
	got := gather(t, newTestExporter(t, api.URL))

	info := got["kube_pod_info"]
	if len(info) != 1 {
		t.Fatalf("kube_pod_info: got %d series, want 1", len(info))
	}
	if p := labelOf(info[0], "pod"); p != "p1" {
		t.Errorf("pod = %q, want \"p1\"", p)
	}
	if ns := labelOf(info[0], "namespace"); ns != "ns1" {
		t.Errorf("namespace = %q, want \"ns1\"", ns)
	}
	if k := labelOf(info[0], "created_by_kind"); k != "DaemonSet" {
		t.Errorf("created_by_kind = %q, want \"DaemonSet\"", k)
	}

	restarts := got["kube_pod_container_status_restarts_total"]
	if len(restarts) != 1 {
		t.Fatalf("restarts: got %d series, want 1", len(restarts))
	}
	if v := restarts[0].GetCounter().GetValue(); v != 3 {
		t.Errorf("restarts = %v, want 3", v)
	}
}

// DaemonSet 은 노드마다 한 벌 돈다. 각 파드가 클러스터 전체 파드를 내면 같은
// 시리즈가 노드 수만큼 생긴다 — 서버 쪽에서 자기 노드로 좁혀야 한다.
func TestOnlyThisNodesPodsAreRequested(t *testing.T) {
	api := newFakeAPI(t)
	gather(t, newTestExporter(t, api.URL))

	q, err := url.ParseQuery(api.podQuery)
	if err != nil {
		t.Fatalf("parse query %q: %v", api.podQuery, err)
	}
	if got := q.Get("fieldSelector"); got != "spec.nodeName=e999" {
		t.Errorf("pod list fieldSelector = %q, want \"spec.nodeName=e999\"; "+
			"without it every node's agent reports every pod and the series multiply by the node count", got)
	}
}

// 도달 불가한 API 에 그럴듯한 기본값을 채우면, 관측 스택은 그것을 사실로 받아
// 저장한다. 아무것도 내지 않는 편이 낫다.
func TestNoFabricatedMetricsWhenTheAPIIsUnreachable(t *testing.T) {
	e, err := New(Config{
		Node: "e999", Mode: "node",
		APIURL: "http://127.0.0.1:1", // 아무도 듣지 않는 포트
		Token:  "test-token",
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := gather(t, e)
	for name, series := range got {
		for _, m := range series {
			t.Errorf("%s emitted %d series with the API unreachable; a value invented here "+
				"becomes an assertion about the cluster that nothing can distinguish from a real one",
				name, len(m.GetLabel()))
			break
		}
	}
}

func TestAPIErrorStatusDoesNotBecomeMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	got := gather(t, newTestExporter(t, srv.URL))
	if len(got) != 0 {
		t.Errorf("got %d metric families on HTTP 403; RBAC being wrong must look like missing data, not like a healthy cluster", len(got))
	}
}

// cluster 모드는 DaemonSet 에서 성립하지 않는다 — 노드마다 한 벌씩 전역 수집을
// 하면 모든 시리즈가 노드 수만큼 중복된다. 조용히 node 로 강등하지 않고 거부해서,
// 설정한 사람이 그 사실을 알게 한다.
func TestClusterModeIsRefusedAtStartup(t *testing.T) {
	_, err := New(Config{Node: "e999", Mode: "cluster", APIURL: "http://example.invalid", Token: "t"})
	if err == nil {
		t.Fatal("cluster mode was accepted; every node would then report the same cluster-wide series")
	}
	if !strings.Contains(err.Error(), "cluster") {
		t.Errorf("error %q does not name the offending mode", err)
	}
}

func TestUnknownModeIsRefused(t *testing.T) {
	if _, err := New(Config{Node: "e999", Mode: "nodes", APIURL: "http://example.invalid", Token: "t"}); err == nil {
		t.Error("typo'd mode was accepted; it would silently collect something other than what was asked for")
	}
}

// 토큰이 없으면 인증이 성립하지 않는다. 기동 시점에 말해 주지 않으면 증상은
// "메트릭이 안 나온다" 뿐이라 원인까지 도달하는 데 시간이 걸린다.
func TestMissingTokenIsRefusedAtStartup(t *testing.T) {
	if _, err := New(Config{Node: "e999", Mode: "node", APIURL: "http://example.invalid"}); err == nil {
		t.Error("empty token was accepted; the failure would only surface as absent metrics")
	}
}
