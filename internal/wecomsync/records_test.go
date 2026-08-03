// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package wecomsync

import "testing"

func TestNormalizeRecordExtractsTextFromSmartSheetValues(t *testing.T) {
	record := NormalizeRecord(map[string]any{
		"record_id": "rec-1",
		"values": map[string]any{
			"name": []any{
				map[string]any{"text": "官网"},
			},
			"target":  map[string]any{"value": "https://matrixorigin.cn/"},
			"enabled": true,
			"port":    float64(30009),
		},
	})

	if record.ID != "rec-1" {
		t.Fatalf("ID = %q", record.ID)
	}
	if record.Fields["name"] != "官网" {
		t.Fatalf("name = %q", record.Fields["name"])
	}
	if record.Fields["target"] != "https://matrixorigin.cn/" {
		t.Fatalf("target = %q", record.Fields["target"])
	}
	if record.Fields["enabled"] != "true" {
		t.Fatalf("enabled = %q", record.Fields["enabled"])
	}
	if record.Fields["port"] != "30009" {
		t.Fatalf("port = %q", record.Fields["port"])
	}
}

func TestNormalizeRecordConcatenatesRichTextFragments(t *testing.T) {
	record := NormalizeRecord(map[string]any{
		"record_id": "rec-url",
		"values": map[string]any{
			"target": []any{
				map[string]any{"text": "https://"},
				map[string]any{"text": "bgem3.model.shanghai.idc.matrixorigin.cn"},
				map[string]any{"text": ":30443"},
			},
		},
	})

	want := "https://bgem3.model.shanghai.idc.matrixorigin.cn:30443"
	if record.Fields["target"] != want {
		t.Fatalf("target = %q, want %q", record.Fields["target"], want)
	}
}
