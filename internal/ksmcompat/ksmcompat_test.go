package ksmcompat

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestKSMCompatExporter_NodeMode(t *testing.T) {
	exp := New(Config{Node: "node-test-1", Mode: "node"})
	reg := prometheus.NewRegistry()
	reg.MustRegister(exp)

	count, err := testutil.GatherAndCount(reg, "kube_node_info", "kube_pod_info", "kube_node_status_condition")
	if err != nil {
		t.Fatalf("failed to gather ksm metrics: %v", err)
	}

	if count < 3 {
		t.Fatalf("expected at least 3 ksm metrics, got %d", count)
	}
}

func TestKSMCompatExporter_ClusterMode(t *testing.T) {
	exp := New(Config{Node: "node-test-1", Mode: "cluster"})
	reg := prometheus.NewRegistry()
	reg.MustRegister(exp)

	count, err := testutil.GatherAndCount(reg, "kube_deployment_status_replicas", "kube_daemonset_status_number_ready")
	if err != nil {
		t.Fatalf("failed to gather cluster ksm metrics: %v", err)
	}

	if count < 2 {
		t.Fatalf("expected at least 2 cluster metrics, got %d", count)
	}
}
