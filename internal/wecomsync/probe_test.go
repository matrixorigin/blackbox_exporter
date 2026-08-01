// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package wecomsync

import "testing"

func TestRecordsToProbesBuildsProbeFromCoreColumnsAndIgnoresExtraFields(t *testing.T) {
	cfg := testConfig()
	records := []SheetRecord{
		{
			ID: "rec-1",
			Fields: map[string]string{
				"enabled":     "TRUE",
				"module":      "http_2xx",
				"target":      "https://matrixorigin.cn/",
				"owner":       "ignored",
				"description": "ignored",
			},
		},
		{
			ID: "rec-2",
			Fields: map[string]string{
				"enabled": "false",
				"module":  "tcp_connect",
				"target":  "127.0.0.1:80",
			},
		},
	}

	probes, err := RecordsToProbes(records, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) != 1 {
		t.Fatalf("len(probes) = %d, want 1", len(probes))
	}
	probe := probes[0]
	if probe.Name != "wecom-matrixorigin-cn" {
		t.Fatalf("name = %q", probe.Name)
	}
	if probe.Module != "http_2xx" || probe.Target != "https://matrixorigin.cn/" {
		t.Fatalf("unexpected probe: %+v", probe)
	}
	if probe.Interval != "30s" || probe.ScrapeTimeout != "10s" {
		t.Fatalf("unexpected timings: %+v", probe)
	}
	if probe.Labels["source"] != "wecom-smartsheet" || len(probe.Labels) != 1 {
		t.Fatalf("labels = %#v", probe.Labels)
	}
}

func TestRecordsToProbesSkipsRowsUnlessEnabledIsExplicitlyTrue(t *testing.T) {
	cfg := testConfig()
	records := []SheetRecord{
		{
			ID: "empty-enabled",
			Fields: map[string]string{
				"module": "http_2xx",
				"target": "https://matrixorigin.cn/",
			},
		},
		{
			ID: "unknown-enabled",
			Fields: map[string]string{
				"enabled": "maybe",
				"module":  "tcp_connect",
				"target":  "example.com:443",
			},
		},
		{
			ID: "explicit-enabled",
			Fields: map[string]string{
				"enabled": "TrUe",
				"module":  "tcp_connect",
				"target":  "shanghai.idc.matrixorigin.cn:30009",
			},
		},
	}

	probes, err := RecordsToProbes(records, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) != 1 {
		t.Fatalf("len(probes) = %d, want 1", len(probes))
	}
	if probes[0].Target != "shanghai.idc.matrixorigin.cn:30009" {
		t.Fatalf("target = %q", probes[0].Target)
	}
}

func TestRecordsToProbesUsesOptionalOverridesWhenPresent(t *testing.T) {
	cfg := testConfig()
	probes, err := RecordsToProbes([]SheetRecord{{
		ID: "rec-1",
		Fields: map[string]string{
			"enabled":        "true",
			"name":           "MatrixOrigin 官网",
			"module":         "http_2xx",
			"target":         "https://matrixorigin.cn/",
			"interval":       "1m",
			"scrape_timeout": "15s",
			"team":           "sre",
			"labels":         "service=website,env=prod",
		},
	}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	probe := probes[0]
	if probe.Name != "wecom-matrixorigin" {
		t.Fatalf("name = %q", probe.Name)
	}
	if probe.Interval != "1m" || probe.ScrapeTimeout != "15s" {
		t.Fatalf("unexpected timings: %+v", probe)
	}
	if probe.Labels["service"] != "website" || probe.Labels["env"] != "prod" || probe.Labels["team"] != "sre" {
		t.Fatalf("labels = %#v", probe.Labels)
	}
}

func TestRecordsToProbesRejectsUnsupportedModule(t *testing.T) {
	cfg := testConfig()
	_, err := RecordsToProbes([]SheetRecord{{
		ID: "rec-1",
		Fields: map[string]string{
			"enabled": "true",
			"name":    "bad",
			"module":  "smtp",
			"target":  "example.com:25",
		},
	}}, cfg)
	if err == nil {
		t.Fatal("expected unsupported module error")
	}
}

func TestProbeObjectUsesPrometheusOperatorProbeShape(t *testing.T) {
	cfg := testConfig()
	probe := ProbeSpec{
		Name:          "wecom-bgem3-public",
		JobName:       "wecom-bgem3-public",
		Module:        "tcp_connect",
		Target:        "bgem3.model.shanghai.idc.matrixorigin.cn:443",
		Interval:      "30s",
		ScrapeTimeout: "10s",
		Labels:        map[string]string{"service": "baai-bge-m3"},
	}

	obj := ProbeObject(probe, cfg)
	if obj.GetAPIVersion() != "monitoring.coreos.com/v1" || obj.GetKind() != "Probe" {
		t.Fatalf("unexpected type: %s %s", obj.GetAPIVersion(), obj.GetKind())
	}
	if obj.GetNamespace() != "mo-ob" || obj.GetLabels()["release"] != "mo-ob-opensource-tke" {
		t.Fatalf("unexpected metadata: namespace=%s labels=%#v", obj.GetNamespace(), obj.GetLabels())
	}

	spec := obj.Object["spec"].(map[string]any)
	if spec["module"] != "tcp_connect" {
		t.Fatalf("module = %v", spec["module"])
	}
	prober := spec["prober"].(map[string]any)
	if prober["url"] != "ai-vllm-blackbox-exporter.mo-ob.svc:9115" {
		t.Fatalf("prober.url = %v", prober["url"])
	}
}

func testConfig() Config {
	cfg := Config{
		WeCom: WeComConfig{
			CorpID:     "corp",
			CorpSecret: "secret",
			DocID:      "doc",
			SheetID:    "sheet",
		},
		Kubernetes: KubernetesConfig{
			ProberURL: "ai-vllm-blackbox-exporter.mo-ob.svc:9115",
		},
		Columns: ColumnConfig{
			LabelColumns: []string{"team"},
		},
	}
	return cfg.WithDefaults()
}
