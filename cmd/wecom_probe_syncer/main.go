// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"

	"github.com/prometheus/blackbox_exporter/internal/wecomsync"
)

func main() {
	os.Exit(run())
}

func run() int {
	configFile := kingpin.Flag("config.file", "WeCom probe syncer configuration file.").Default("wecom-probe-syncer.yml").String()
	once := kingpin.Flag("once", "Run one synchronization and exit.").Bool()
	dryRun := kingpin.Flag("dry-run", "Fetch and render desired probes without writing to Kubernetes.").Bool()
	promslogConfig := &promslog.Config{}
	flag.AddFlags(kingpin.CommandLine, promslogConfig)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()

	logger := promslog.New(promslogConfig)
	cfg, err := wecomsync.LoadConfig(*configFile)
	if err != nil {
		logger.Error("Failed to load config", "err", err)
		return 1
	}

	source, err := wecomsync.NewWeComClient(cfg)
	if err != nil {
		logger.Error("Failed to create WeCom client", "err", err)
		return 1
	}

	var store wecomsync.ProbeStore
	if *dryRun {
		store = &wecomsync.DryRunProbeStore{Config: cfg}
	} else {
		store, err = wecomsync.NewKubernetesProbeStore(cfg)
		if err != nil {
			logger.Error("Failed to create Kubernetes probe store", "err", err)
			return 1
		}
	}

	syncer := &wecomsync.Synchronizer{
		Source: source,
		Store:  store,
		Config: cfg,
		Logger: logger,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := syncOnce(ctx, syncer, logger); err != nil {
		return 1
	}
	if *once {
		return 0
	}

	ticker := time.NewTicker(cfg.SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("Stopping WeCom probe syncer")
			return 0
		case <-ticker.C:
			if err := syncOnce(ctx, syncer, logger); err != nil {
				logger.Error("Synchronization failed", "err", err)
			}
		}
	}
}

func syncOnce(ctx context.Context, syncer *wecomsync.Synchronizer, logger *slog.Logger) error {
	result, err := syncer.SyncOnce(ctx)
	if err != nil {
		logger.Error("Synchronization failed", "err", err)
		return err
	}
	logger.Info("Synchronization completed", "applied", result.Applied, "deleted", result.Deleted, "skipped", result.Skipped)
	return nil
}
