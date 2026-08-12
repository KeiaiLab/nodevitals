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
	"github.com/KeiaiLab/nodevitals/internal/history"
	"github.com/KeiaiLab/nodevitals/internal/httpapi"
	"github.com/KeiaiLab/nodevitals/internal/ksmcompat"
	"github.com/KeiaiLab/nodevitals/internal/nodecompat"
	"github.com/KeiaiLab/nodevitals/internal/nodeexporter"
	"github.com/KeiaiLab/nodevitals/internal/sink"
	"github.com/KeiaiLab/nodevitals/internal/smartctlcompat"
)

// version 은 빌드 시 -ldflags "-X main.version=..." 로 주입된다. 소스에 릴리스
// 번호를 적어 두면 bump 를 잊는 순간 이미지가 자기 버전을 틀리게 신고하고,
// 그것이 배포 검증의 유일한 자기신고 수단이라 확인할 방법 자체가 사라진다.
// 주입이 없으면 "unknown" 으로 남는다 — 모르는 것을 모른다고 말하는 편이,
// 아닐 수도 있는 릴리스를 자칭하는 것보다 낫다.
var version string

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
			reg.Add(collector.NewHeartbeat(cfg.Node, version))
			reg.Add(collector.NewLoadAvg(cfg.Node, cfg.ProcRoot))
			reg.Add(collector.NewCPU(cfg.Node, cfg.ProcRoot))
			reg.Add(collector.NewMem(cfg.Node, cfg.ProcRoot))
			reg.Add(collector.NewNet(cfg.Node, cfg.ProcRoot))
			reg.Add(collector.NewDisk(cfg.Node, cfg.ProcRoot, cfg.SysRoot))
			reg.Add(collector.NewHwmon(cfg.Node, cfg.SysRoot))
			reg.Add(collector.NewPSI(cfg.Node, cfg.ProcRoot))
			reg.Add(collector.NewPower(cfg.Node, cfg.SysRoot))
			reg.Add(collector.NewPCIeAER(cfg.Node, cfg.SysRoot))
		case "smart":
			// Registered only when the smart tier runs: a node whose disks the
			// probe cannot read then serves zero smartctl_* series, exactly
			// like the smartctl_exporter DaemonSet that finds nothing there.
			var sc *smartctlcompat.Exporter
			if cfg.SmartctlCompat.Enabled {
				sc = smartctlcompat.New()
				if err := metrics.Register(sc); err != nil {
					slog.Error("register smartctl compat exporter", "err", err)
					os.Exit(1)
				}
				slog.Info("smartctl compat surface enabled")
			}
			reg.Add(collector.NewSmart(cfg.Node, collector.NewDevProbe(cfg.DevRoot), sc))
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
		extraFlags := nodeExporterFlags(cfg.NodeExporter)
		if cfg.NodeExporter.NativeCollectors {
			nc := nodecompat.New(cfg.ProcRoot, cfg.SysRoot, cfg.NodeExporter.RootFSPath, slog.Default())
			if err := metrics.Register(nc); err != nil {
				slog.Error("register native nodecompat exporter", "err", err)
				os.Exit(1)
			}
			slog.Info("native nodecompat collectors registered")
		}
		c, err := nodeexporter.New(nodeexporter.Config{
			ProcPath:    cfg.ProcRoot,
			SysPath:     cfg.SysRoot,
			RootFSPath:  cfg.NodeExporter.RootFSPath,
			TextfileDir: cfg.NodeExporter.TextfileDir,
			ExtraFlags:  extraFlags,
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

	// kube_* 표면. 설정이 잘못됐거나(cluster 모드·오타) 토큰이 없으면 여기서
	// 멈춘다 — 그 상태로 계속 돌면 증상이 "메트릭이 안 나온다" 하나뿐이라
	// 원인까지 도달하는 데 시간이 걸린다.
	if cfg.KSMCompat.Enabled {
		ksm, err := ksmcompat.New(ksmcompat.Config{
			Node: cfg.Node,
			Mode: cfg.KSMCompat.Mode,
			Log:  slog.Default(),
		})
		if err != nil {
			slog.Error("ksm compat surface", "err", err)
			os.Exit(1)
		}
		if err := metrics.Register(ksm); err != nil {
			slog.Error("register ksm compat exporter", "err", err)
			os.Exit(1)
		}
		slog.Info("ksm compat surface enabled", "mode", "node", "scope", "this node and its pods")
	}

	// Long-term downsampled history — local to this node, survives past the
	// Prometheus scrape retention window. Opening failure is fatal (not a
	// silent skip): the operator explicitly asked for history, and a
	// mis-mounted hostPath failing here beats months of quietly-missing data
	// discovered only when someone finally queries it.
	var hist *history.Store
	if cfg.History.Enabled {
		hist, err = history.Open(cfg.History.DataDir, cfg.History.Metrics,
			time.Duration(cfg.History.IntervalMinutes)*time.Minute,
			time.Duration(cfg.History.RetentionDays)*24*time.Hour)
		if err != nil {
			slog.Error("open history store", "dataDir", cfg.History.DataDir, "err", err)
			os.Exit(1)
		}
		defer hist.Close()
		slog.Info("history store enabled", "dataDir", cfg.History.DataDir,
			"retentionDays", cfg.History.RetentionDays, "intervalMinutes", cfg.History.IntervalMinutes,
			"metrics", strings.Join(cfg.History.Metrics, ","))
	}

	a := agent.New(cfg, &reg, eng, webhooks, metrics, hist)

	mux := httpapi.NewServer(a, metrics.Handler(), a)
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

// nodeExporterFlags 는 임베드 node_exporter 에 넘길 collector 플래그를 만든다.
//
// 자체 수집기가 켜지면 그것이 대체하는 upstream collector 를 **전부** 꺼야 한다.
// 하나라도 남으면 같은 메트릭 이름이 두 곳에서 등록되고, client_golang 은 충돌한
// family 를 스크레이프 결과에서 빼면서도 200 을 계속 준다 — 파드는 Ready, /metrics
// 는 정상, 그 시리즈만 조용히 사라진다.
//
// 차단 목록은 nodecompat 이 자기 수집기 집합에서 파생시킨다. 여기에 이름을 다시
// 적으면 nodecompat 에 수집기가 추가될 때마다 두 목록이 어긋난다 — 실제로 0.9.0 이
// loadavg·uname 둘만 적어 나머지 다섯(entropy·filefd·stat·vmstat·os)이 중복됐다.
func nodeExporterFlags(cfg config.NodeExporterConfig) []string {
	if !cfg.NativeCollectors {
		return cfg.ExtraFlags
	}
	// cfg.ExtraFlags 에 그대로 append 하면 cap 여유가 있을 때 호출자의 배열에
	// 써 들어간다. config 는 한 번 읽어 계속 쓰이므로 복사해서 시작한다.
	native := nodecompat.NoCollectorFlags()
	flags := make([]string, 0, len(cfg.ExtraFlags)+len(native))
	flags = append(flags, cfg.ExtraFlags...)
	return append(flags, native...)
}
