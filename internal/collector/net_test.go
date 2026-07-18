package collector

import (
	"context"
	"testing"
)

func TestNetReadsFixtureSkipsLoopback(t *testing.T) {
	c := NewNet("n", "../../testdata/proc")
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	byDev := map[string]map[string]float64{}
	for _, s := range got {
		if byDev[s.Device] == nil {
			byDev[s.Device] = map[string]float64{}
		}
		byDev[s.Device][s.Metric] = s.Value
	}
	if _, ok := byDev["lo"]; ok {
		t.Fatal("loopback must be skipped")
	}
	if byDev["eth0"]["net_rx_bytes"] != 5000 || byDev["eth0"]["net_tx_bytes"] != 6000 {
		t.Fatalf("eth0 bytes wrong: %+v", byDev["eth0"])
	}
	if byDev["eth0"]["net_rx_errors"] != 2 || byDev["eth0"]["net_tx_errors"] != 1 {
		t.Fatalf("eth0 errors wrong: %+v", byDev["eth0"])
	}
}
