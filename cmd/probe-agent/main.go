package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Handgrip/global-telemetry/internal/config"
	"github.com/Handgrip/global-telemetry/internal/reporter"
	"github.com/Handgrip/global-telemetry/internal/scheduler"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "/etc/probe-agent/agent.yaml", "path to agent config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	if *showVersion {
		fmt.Println("probe-agent", version)
		os.Exit(0)
	}

	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	slog.Info("starting probe-agent", "version", version, "config", *configPath)

	agentCfg, err := config.LoadAgentConfig(*configPath)
	if err != nil {
		slog.Error("failed to load agent config", "error", err)
		os.Exit(1)
	}

	slog.Info("agent config loaded",
		"probe_name", agentCfg.ProbeName,
		"config_url", agentCfg.ConfigURL,
		"push_interval", agentCfg.PushInterval,
	)

	cm := config.NewConfigManager(agentCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := cm.InitialLoad(ctx); err != nil {
		slog.Error("failed to load initial targets config", "error", err)
		os.Exit(1)
	}

	targets := cm.GetTargets()
	slog.Info("targets loaded", "count", len(targets.Targets))

	rep := reporter.NewPrometheusReporter(agentCfg.GrafanaCloud, agentCfg.ProbeName, agentCfg.MetricPrefix)
	sched := scheduler.New(cm, rep, agentCfg.GetPushInterval())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	sched.Run(ctx)
	slog.Info("probe-agent stopped")
}
