// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package wecomsync

import (
	"strings"
	"testing"
)

func TestConfigAcceptsMultipleSheetIDs(t *testing.T) {
	cfg := Config{
		WeCom: WeComConfig{
			CorpID:     "corp",
			CorpSecret: "secret",
			DocID:      "doc",
			SheetIDs:   []string{"sheet-a", "sheet-b"},
		},
		Kubernetes: KubernetesConfig{
			ProberURL: "blackbox-exporter.mo-ob.svc:9115",
		},
	}.WithDefaults()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := cfg.WeComSheetIDs(); strings.Join(got, ",") != "sheet-a,sheet-b" {
		t.Fatalf("WeComSheetIDs() = %#v", got)
	}
}

func TestConfigKeepsLegacySingleSheetID(t *testing.T) {
	cfg := Config{
		WeCom: WeComConfig{
			CorpID:     "corp",
			CorpSecret: "secret",
			DocID:      "doc",
			SheetID:    "legacy-sheet",
		},
		Kubernetes: KubernetesConfig{
			ProberURL: "blackbox-exporter.mo-ob.svc:9115",
		},
	}.WithDefaults()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := cfg.WeComSheetIDs(); len(got) != 1 || got[0] != "legacy-sheet" {
		t.Fatalf("WeComSheetIDs() = %#v", got)
	}
}
