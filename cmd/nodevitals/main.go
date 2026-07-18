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
	"syscall"
	"time"

	"github.com/KeiaiLab/nodevitals/internal/agent"
	"github.com/KeiaiLab/nodevitals/internal/collector"
	"github.com/KeiaiLab/nodevitals/internal/config"
	"github.com/KeiaiLab/nodevitals/internal/event"
	"github.com/KeiaiLab/nodevitals/internal/httpapi"
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

	var reg collector.Registry
	reg.Add(collector.NewLoadAvg(cfg.Node, cfg.ProcRoot))
	reg.Add(collector.NewCPU(cfg.Node, cfg.ProcRoot))
	reg.Add(collector.NewMem(cfg.Node, cfg.ProcRoot))
	reg.Add(collector.NewNet(cfg.Node, cfg.ProcRoot))
	reg.Add(collector.NewDisk(cfg.Node, cfg.ProcRoot, cfg.SysRoot))
	reg.Add(collector.NewHwmon(cfg.Node, cfg.SysRoot))

	eng := event.NewEngine(cfg.Node, cfg.Rules)

	var webhooks []sink.Sink
	for _, w := range cfg.Sinks.Webhook {
		webhooks = append(webhooks, sink.NewWebhook(w, nil))
	}
	metrics := sink.NewMetrics()

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
	slog.Info("nodevitals started", "node", cfg.Node, "tier", cfg.Tier, "listen", listen)
	a.Run(ctx)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("http shutdown", "err", err)
	}
}
