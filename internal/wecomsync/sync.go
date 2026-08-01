// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package wecomsync

import (
	"context"
	"fmt"
	"log/slog"
)

type RecordSource interface {
	Records(ctx context.Context) ([]SheetRecord, error)
}

type ProbeStore interface {
	Apply(ctx context.Context, probe ProbeSpec) error
	ListManaged(ctx context.Context) ([]string, error)
	Delete(ctx context.Context, name string) error
}

type Synchronizer struct {
	Source RecordSource
	Store  ProbeStore
	Config Config
	Logger *slog.Logger
}

type SyncResult struct {
	Applied int
	Deleted int
	Skipped int
}

func (s *Synchronizer) SyncOnce(ctx context.Context) (SyncResult, error) {
	if s.Source == nil {
		return SyncResult{}, fmt.Errorf("record source is nil")
	}
	if s.Store == nil {
		return SyncResult{}, fmt.Errorf("probe store is nil")
	}
	records, err := s.Source.Records(ctx)
	if err != nil {
		return SyncResult{}, fmt.Errorf("fetch records: %w", err)
	}
	probes, err := RecordsToProbes(records, s.Config)
	if err != nil {
		return SyncResult{}, fmt.Errorf("map records to probes: %w", err)
	}

	desired := map[string]struct{}{}
	for _, probe := range probes {
		if err := s.Store.Apply(ctx, probe); err != nil {
			return SyncResult{}, fmt.Errorf("apply probe %q: %w", probe.Name, err)
		}
		desired[probe.Name] = struct{}{}
	}

	result := SyncResult{Applied: len(probes), Skipped: len(records) - len(probes)}
	if s.Config.Kubernetes.Prune != nil && !*s.Config.Kubernetes.Prune {
		return result, nil
	}

	existing, err := s.Store.ListManaged(ctx)
	if err != nil {
		return SyncResult{}, fmt.Errorf("list managed probes: %w", err)
	}
	for _, name := range existing {
		if _, ok := desired[name]; ok {
			continue
		}
		if err := s.Store.Delete(ctx, name); err != nil {
			return SyncResult{}, fmt.Errorf("delete stale probe %q: %w", name, err)
		}
		result.Deleted++
	}

	if s.Logger != nil {
		s.Logger.Info("Synchronized WeCom probes", "applied", result.Applied, "deleted", result.Deleted, "skipped", result.Skipped)
	}
	return result, nil
}

type StaticRecordSource struct {
	Items []SheetRecord
}

func (s StaticRecordSource) Records(context.Context) ([]SheetRecord, error) {
	return s.Items, nil
}
