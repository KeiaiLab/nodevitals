// Package ksmcompat serves a kube-state-metrics-compatible kube_* surface for
// the objects this node can speak for: the node itself and the pods scheduled
// onto it.
//
// Scope is deliberate. kube-state-metrics runs as a single Deployment and
// reports the whole cluster; nodevitals runs as a DaemonSet, one copy per node.
// A per-node copy reporting cluster-wide objects would emit the same series
// once per node, so this package only reports node-scoped facts and refuses any
// other mode outright rather than quietly collecting something else.
//
// Every value here comes from the API server. Nothing is defaulted, filled in,
// or approximated: a plausible number invented on this side becomes an
// assertion about the cluster that no consumer can tell apart from a real
// measurement, and alert rules act on it.
package ksmcompat

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	defaultAPIURL   = "https://kubernetes.default.svc"
	tokenPath       = "/var/run/secrets/kubernetes.io/serviceaccount/token" // #nosec G101 -- well-known in-cluster path, not a credential
	caPath          = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	defaultCacheTTL = 30 * time.Second
	requestTimeout  = 5 * time.Second
)

var (
	nodeInfoDesc = prometheus.NewDesc(
		"kube_node_info",
		"Information about a cluster node.",
		[]string{"node", "kernel_version", "os_image", "container_runtime_version", "kubeproxy_version"},
		nil,
	)
	nodeStatusConditionDesc = prometheus.NewDesc(
		"kube_node_status_condition",
		"The condition of a cluster node.",
		[]string{"node", "condition", "status"},
		nil,
	)
	nodeStatusCapacityDesc = prometheus.NewDesc(
		"kube_node_status_capacity",
		"The capacity for different resources of a cluster node.",
		[]string{"node", "resource", "unit"},
		nil,
	)
	nodeStatusAllocatableDesc = prometheus.NewDesc(
		"kube_node_status_allocatable",
		"The allocatable for different resources of a cluster node.",
		[]string{"node", "resource", "unit"},
		nil,
	)
	podInfoDesc = prometheus.NewDesc(
		"kube_pod_info",
		"Information about pod.",
		[]string{"pod", "namespace", "host_ip", "pod_ip", "node", "created_by_kind", "created_by_name"},
		nil,
	)
	podStatusPhaseDesc = prometheus.NewDesc(
		"kube_pod_status_phase",
		"The pods current phase.",
		[]string{"pod", "namespace", "phase"},
		nil,
	)
	podContainerReadyDesc = prometheus.NewDesc(
		"kube_pod_container_status_ready",
		"Describes whether the container is ready.",
		[]string{"container", "pod", "namespace"},
		nil,
	)
	podContainerRestartsDesc = prometheus.NewDesc(
		"kube_pod_container_status_restarts_total",
		"The number of container restarts.",
		[]string{"container", "pod", "namespace"},
		nil,
	)
	// Carrier for scrape failures. Handing this to the channel makes
	// client_golang surface the error through the scrape's error path instead
	// of the endpoint quietly serving fewer series than it did a minute ago.
	scrapeErrorDesc = prometheus.NewDesc(
		"kube_state_metrics_scrape_error",
		"Placeholder that carries a ksmcompat scrape failure to the registry.",
		nil, nil,
	)
)

// podPhases and nodeConditionStates are emitted in full for every object, one
// series per possible value with only the current one set to 1. That is what
// kube-state-metrics does, and queries depend on it: a rule matching
// phase="Failed" needs the series to exist and read 0, not to be absent.
var podPhases = []string{"Pending", "Running", "Succeeded", "Failed", "Unknown"}
var nodeConditionStates = []string{"true", "false", "unknown"}

// Config configures the ksmcompat Exporter.
type Config struct {
	Node   string
	Mode   string // "node" only — see the package comment
	APIURL string // defaults to the in-cluster API service
	Token  string // defaults to the mounted service-account token
	CAPath string // defaults to the mounted service-account CA
	Log    *slog.Logger
	// CacheTTL bounds how often the API is queried. Scrapes are far more
	// frequent than these objects change, and every node runs a copy.
	CacheTTL time.Duration
}

// Exporter collects kube_* metrics and satisfies prometheus.Collector.
type Exporter struct {
	node   string
	apiURL string
	token  string
	client *http.Client
	log    *slog.Logger
	ttl    time.Duration

	mu        sync.Mutex
	cached    *snapshot
	lastFetch time.Time
}

type snapshot struct {
	node *nodeObj
	pods []podObj
}

// New returns a new ksmcompat Exporter, or an error if it could not be
// configured to actually reach the API. Refusing at startup is deliberate:
// every failure mode here shows up at scrape time as "the metrics are just not
// there", which is a long way from the cause.
func New(cfg Config) (*Exporter, error) {
	switch cfg.Mode {
	case "", "node":
		// ok
	case "cluster":
		return nil, fmt.Errorf(`ksmCompat mode "cluster" is not supported: nodevitals runs as a DaemonSet, ` +
			`so a cluster-wide collection would be performed once per node and every series would be ` +
			`duplicated by the node count. Use mode "node", and keep kube-state-metrics for cluster-scoped objects`)
	default:
		return nil, fmt.Errorf("ksmCompat mode %q is not known (want \"node\")", cfg.Mode)
	}

	if cfg.Node == "" {
		return nil, fmt.Errorf("ksmCompat needs the node name: every series it emits is scoped to one node")
	}

	token := cfg.Token
	if token == "" {
		data, err := os.ReadFile(tokenPath)
		if err != nil {
			return nil, fmt.Errorf("read service-account token at %s: %w "+
				"(the pod needs a ServiceAccount with automountServiceAccountToken enabled)", tokenPath, err)
		}
		token = string(data)
	}
	if token == "" {
		return nil, fmt.Errorf("service-account token is empty")
	}

	apiURL := cfg.APIURL
	if apiURL == "" {
		apiURL = defaultAPIURL
	}

	transport, err := transportFor(apiURL, cfg.CAPath)
	if err != nil {
		return nil, err
	}

	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	ttl := cfg.CacheTTL
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}

	return &Exporter{
		node:   cfg.Node,
		apiURL: apiURL,
		token:  token,
		client: &http.Client{Timeout: requestTimeout, Transport: transport},
		log:    log,
		ttl:    ttl,
	}, nil
}

// transportFor pins the API server's CA when talking HTTPS. Falling back to
// InsecureSkipVerify would make an intercepted connection indistinguishable
// from the real API server, and this client sends a bearer token.
func transportFor(apiURL, caPathOverride string) (http.RoundTripper, error) {
	u, err := url.Parse(apiURL)
	if err != nil {
		return nil, fmt.Errorf("parse apiURL %q: %w", apiURL, err)
	}
	if u.Scheme != "https" {
		return http.DefaultTransport, nil
	}

	path := caPathOverride
	if path == "" {
		path = caPath
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read API server CA at %s: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificate found in %s", path)
	}
	return &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}, nil
}

// Describe satisfies prometheus.Collector. Nothing is described: the series
// depend on what the API returns, and an unchecked collector is what lets a
// failed scrape emit nothing at all rather than a fabricated skeleton.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {}

// Collect satisfies prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	snap, err := e.load()
	if err != nil {
		e.log.Warn("ksmcompat scrape failed — emitting no kube_* series", "node", e.node, "err", err)
		ch <- prometheus.NewInvalidMetric(scrapeErrorDesc, err)
		return
	}
	e.collectNode(ch, snap.node)
	e.collectPods(ch, snap.pods)
}

func (e *Exporter) load() (*snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cached != nil && time.Since(e.lastFetch) < e.ttl {
		return e.cached, nil
	}

	var node nodeObj
	if err := e.get("/api/v1/nodes/"+url.PathEscape(e.node), nil, &node); err != nil {
		return nil, fmt.Errorf("get node %s: %w", e.node, err)
	}
	var pods podList
	q := url.Values{"fieldSelector": {"spec.nodeName=" + e.node}}
	if err := e.get("/api/v1/pods", q, &pods); err != nil {
		return nil, fmt.Errorf("list pods on %s: %w", e.node, err)
	}

	e.cached = &snapshot{node: &node, pods: pods.Items}
	e.lastFetch = time.Now()
	return e.cached, nil
}

func (e *Exporter) get(path string, query url.Values, into any) error {
	u := e.apiURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Accept", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 403 은 RBAC 이 모자란다는 뜻이고, 그 상태에서 메트릭을 지어내면
		// 권한 문제가 "건강한 클러스터" 처럼 보이게 된다.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, path, body)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

func (e *Exporter) collectNode(ch chan<- prometheus.Metric, n *nodeObj) {
	ni := n.Status.NodeInfo
	ch <- prometheus.MustNewConstMetric(nodeInfoDesc, prometheus.GaugeValue, 1,
		e.node, ni.KernelVersion, ni.OSImage, ni.ContainerRuntimeVersion, ni.KubeProxyVersion)

	for _, c := range n.Status.Conditions {
		current := conditionState(c.Status)
		for _, state := range nodeConditionStates {
			v := 0.0
			if state == current {
				v = 1
			}
			ch <- prometheus.MustNewConstMetric(nodeStatusConditionDesc, prometheus.GaugeValue, v,
				e.node, c.Type, state)
		}
	}

	e.collectQuantities(ch, nodeStatusCapacityDesc, n.Status.Capacity)
	e.collectQuantities(ch, nodeStatusAllocatableDesc, n.Status.Allocatable)
}

// collectQuantities skips resources whose value cannot be parsed rather than
// emitting a zero for them — "this node has 0 memory" is a worse answer than
// "this node did not report memory".
func (e *Exporter) collectQuantities(ch chan<- prometheus.Metric, desc *prometheus.Desc, res map[string]string) {
	for name, raw := range res {
		v, err := parseQuantity(raw)
		if err != nil {
			e.log.Warn("unreadable resource quantity — skipping", "node", e.node, "resource", name, "value", raw, "err", err)
			continue
		}
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v, e.node, name, resourceUnit(name))
	}
}

func (e *Exporter) collectPods(ch chan<- prometheus.Metric, pods []podObj) {
	for _, p := range pods {
		kind, owner := "", ""
		if len(p.Metadata.OwnerReferences) > 0 {
			kind = p.Metadata.OwnerReferences[0].Kind
			owner = p.Metadata.OwnerReferences[0].Name
		}
		ch <- prometheus.MustNewConstMetric(podInfoDesc, prometheus.GaugeValue, 1,
			p.Metadata.Name, p.Metadata.Namespace, p.Status.HostIP, p.Status.PodIP, e.node, kind, owner)

		for _, phase := range podPhases {
			v := 0.0
			if phase == p.Status.Phase {
				v = 1
			}
			ch <- prometheus.MustNewConstMetric(podStatusPhaseDesc, prometheus.GaugeValue, v,
				p.Metadata.Name, p.Metadata.Namespace, phase)
		}

		for _, cs := range p.Status.ContainerStatuses {
			ready := 0.0
			if cs.Ready {
				ready = 1
			}
			ch <- prometheus.MustNewConstMetric(podContainerReadyDesc, prometheus.GaugeValue, ready,
				cs.Name, p.Metadata.Name, p.Metadata.Namespace)
			ch <- prometheus.MustNewConstMetric(podContainerRestartsDesc, prometheus.CounterValue, cs.RestartCount,
				cs.Name, p.Metadata.Name, p.Metadata.Namespace)
		}
	}
}

// conditionState maps the API's "True"/"False"/anything-else to the lowercase
// label value kube-state-metrics uses.
func conditionState(s string) string {
	switch s {
	case "True":
		return "true"
	case "False":
		return "false"
	default:
		return "unknown"
	}
}

// Only the fields this package reports are declared; the API returns far more
// and encoding/json ignores the rest.
type nodeObj struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Status struct {
		NodeInfo struct {
			KernelVersion           string `json:"kernelVersion"`
			OSImage                 string `json:"osImage"`
			ContainerRuntimeVersion string `json:"containerRuntimeVersion"`
			KubeProxyVersion        string `json:"kubeProxyVersion"`
		} `json:"nodeInfo"`
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
		Capacity    map[string]string `json:"capacity"`
		Allocatable map[string]string `json:"allocatable"`
	} `json:"status"`
}

type podObj struct {
	Metadata struct {
		Name            string `json:"name"`
		Namespace       string `json:"namespace"`
		OwnerReferences []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Status struct {
		Phase             string `json:"phase"`
		HostIP            string `json:"hostIP"`
		PodIP             string `json:"podIP"`
		ContainerStatuses []struct {
			Name         string  `json:"name"`
			Ready        bool    `json:"ready"`
			RestartCount float64 `json:"restartCount"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

type podList struct {
	Items []podObj `json:"items"`
}
