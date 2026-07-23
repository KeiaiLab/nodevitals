// Command nodevitals runs the hardware telemetry agent.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/agent"
	"github.com/KeiaiLab/nodevitals/internal/collector"
	"github.com/KeiaiLab/nodevitals/internal/config"
	"github.com/KeiaiLab/nodevitals/internal/dcgmcompat"
	"github.com/KeiaiLab/nodevitals/internal/event"
	"github.com/KeiaiLab/nodevitals/internal/httpapi"
	"github.com/KeiaiLab/nodevitals/internal/nodeexporter"
	"github.com/KeiaiLab/nodevitals/internal/sink"
)

func main() {
	cfgPath := flag.String("config", "/etc/nodevitals/config.yaml", "config file path")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	if cfg.Node == "" {
		cfg.Node = os.Getenv("NODE_NAME") // downward API
	}

	metrics := sink.NewMetrics()

	tiers := cfg.ResolvedTiers()
	var reg collector.Registry
	var dcgm *dcgmcompat.Exporter
	for _, tier := range tiers {
		switch tier {
		case "core":
			reg.Add(collector.NewLoadAvg(cfg.Node, cfg.ProcRoot))
			reg.Add(collector.NewCPU(cfg.Node, cfg.ProcRoot))
			reg.Add(collector.NewMem(cfg.Node, cfg.ProcRoot))
			reg.Add(collector.NewNet(cfg.Node, cfg.ProcRoot))
			reg.Add(collector.NewDisk(cfg.Node, cfg.ProcRoot, cfg.SysRoot))
			reg.Add(collector.NewHwmon(cfg.Node, cfg.SysRoot))
			reg.Add(collector.NewPSI(cfg.Node, cfg.ProcRoot))
			reg.Add(collector.NewPower(cfg.Node, cfg.SysRoot))
		case "smart":
			reg.Add(collector.NewSmart(cfg.Node, collector.NewDevProbe(cfg.DevRoot)))
		case "gpu":
			r, err := collector.NewNVMLReader()
			if err != nil {
				// Running gpu alone means the operator asked for GPU telemetry
				// and nothing else, so a dead NVML is a hard failure and the
				// CrashLoop is the signal. Running it alongside other tiers is
				// the single-pod layout, where the same DaemonSet covers a
				// mixed fleet — a node without a GPU must still deliver core
				// and smart, so drop just this collector.
				if len(tiers) == 1 {
					slog.Error("gpu reader init", "err", err)
					os.Exit(1)
				}
				slog.Warn("gpu reader init failed — skipping gpu tier", "err", err)
				continue
			}
			// Registered only when the reader is live: a GPU-less node then
			// serves zero DCGM_FI_* series, exactly like the dcgm-exporter
			// DaemonSet that never schedules there.
			if cfg.DCGMCompat.Enabled {
				dcgm = dcgmcompat.New(cfg.Node)
				if err := metrics.Register(dcgm); err != nil {
					slog.Error("register dcgm compat exporter", "err", err)
					os.Exit(1)
				}
				slog.Info("dcgm compat surface enabled", "driver", r.DriverVersion())
			}
			reg.Add(collector.NewGPUCollector(cfg.Node, r, dcgm))
		default:
			slog.Error("unknown tier", "tier", tier, "known", "core, smart, gpu")
			os.Exit(1)
		}
	}

	eng := event.NewEngine(cfg.Node, cfg.Rules)

	var webhooks []sink.Sink
	for _, w := range cfg.Sinks.Webhook {
		webhooks = append(webhooks, sink.NewWebhook(w, nil))
	}

	// Serve the upstream node_* surface from this same endpoint when asked, so
	// one DaemonSet replaces a separate node_exporter one and the existing
	// dashboards and alert rules built on node_* keep working untouched.
	neCount := 0
	if cfg.NodeExporter.Enabled {
		c, err := nodeexporter.New(nodeexporter.Config{
			ProcPath:    cfg.ProcRoot,
			SysPath:     cfg.SysRoot,
			RootFSPath:  cfg.NodeExporter.RootFSPath,
			TextfileDir: cfg.NodeExporter.TextfileDir,
			ExtraFlags:  cfg.NodeExporter.ExtraFlags,
		}, slog.Default())
		if err != nil {
			slog.Error("node_exporter collectors", "err", err)
			os.Exit(1)
		}
		if err := metrics.Register(c); err != nil {
			slog.Error("register node_exporter collectors", "err", err)
			os.Exit(1)
		}
		// An empty set means the flags never took effect: the endpoint would
		// serve zero node_* series while looking perfectly healthy, which is
		// exactly the silent failure this project keeps running into.
		names := nodeexporter.Enabled(c)
		neCount = len(names)
		if neCount == 0 {
			slog.Error("node_exporter enabled but no collectors are active — refusing to serve an empty node_* surface")
			os.Exit(1)
		}
		slog.Info("node_exporter collectors registered", "count", neCount)
	}

	a := agent.New(cfg, &reg, eng, webhooks, metrics)

	mux := httpapi.NewServer(a, metrics.Handler())
	listen := cfg.Sinks.Metrics.ListenAddr
	if listen == "" {
		listen = ":9847"
	}
	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server", "err", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	slog.Info("nodevitals started", "node", cfg.Node, "tiers", strings.Join(tiers, ","), "nodeExporterCollectors", neCount, "listen", listen)
	a.Run(ctx)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("http shutdown", "err", err)
	}
}
