// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package wecomsync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWeComClientFetchesAndNormalizesRecords(t *testing.T) {
	var sawTokenRequest, sawRecordsRequest bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			sawTokenRequest = true
			if r.URL.Query().Get("corpid") != "corp" || r.URL.Query().Get("corpsecret") != "secret" {
				t.Fatalf("unexpected token query: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errcode":      0,
				"access_token": "token",
			})
		case "/cgi-bin/wedoc/smartsheet/get_records":
			sawRecordsRequest = true
			if r.URL.Query().Get("access_token") != "token" {
				t.Fatalf("access_token = %q", r.URL.Query().Get("access_token"))
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["docid"] != "doc" || body["sheet_id"] != "sheet" {
				t.Fatalf("unexpected records body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errcode": 0,
				"records": []map[string]any{{
					"record_id": "rec-1",
					"values": map[string]any{
						"name":   "官网",
						"module": "http_2xx",
						"target": "https://matrixorigin.cn/",
					},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &WeComClient{
		BaseURL:    server.URL,
		CorpID:     "corp",
		CorpSecret: "secret",
		DocID:      "doc",
		SheetID:    "sheet",
		KeyType:    "CELL_VALUE_KEY_TYPE_FIELD_TITLE",
		PageSize:   100,
		HTTPClient: server.Client(),
	}
	records, err := client.Records(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !sawTokenRequest || !sawRecordsRequest {
		t.Fatalf("requests: token=%v records=%v", sawTokenRequest, sawRecordsRequest)
	}
	if len(records) != 1 || records[0].Fields["target"] != "https://matrixorigin.cn/" {
		t.Fatalf("records = %#v", records)
	}
}
