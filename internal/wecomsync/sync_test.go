// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package wecomsync

import (
	"context"
	"slices"
	"testing"
)

func TestSynchronizerAppliesDesiredProbesAndPrunesStaleOnes(t *testing.T) {
	store := &fakeProbeStore{existing: []string{"wecom-stale", "wecom-matrixorigin"}}
	syncer := &Synchronizer{
		Source: StaticRecordSource{Items: []SheetRecord{{
			ID: "rec-1",
			Fields: map[string]string{
				"enabled": "true",
				"name":    "matrixorigin",
				"module":  "http_2xx",
				"target":  "https://matrixorigin.cn/",
			},
		}}},
		Store:  store,
		Config: testConfig(),
	}

	result, err := syncer.SyncOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied != 1 || result.Deleted != 1 {
		t.Fatalf("result = %+v", result)
	}
	if !slices.Contains(store.applied, "wecom-matrixorigin") {
		t.Fatalf("applied = %#v", store.applied)
	}
	if !slices.Contains(store.deleted, "wecom-stale") {
		t.Fatalf("deleted = %#v", store.deleted)
	}
}

type fakeProbeStore struct {
	existing []string
	applied  []string
	deleted  []string
}

func (s *fakeProbeStore) Apply(_ context.Context, probe ProbeSpec) error {
	s.applied = append(s.applied, probe.Name)
	return nil
}

func (s *fakeProbeStore) ListManaged(context.Context) ([]string, error) {
	return s.existing, nil
}

func (s *fakeProbeStore) Delete(_ context.Context, name string) error {
	s.deleted = append(s.deleted, name)
	return nil
}
