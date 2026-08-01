// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package wecomsync

import (
	"fmt"
	"sort"
	"strings"
)

type SheetRecord struct {
	ID     string
	Fields map[string]string
}

func NormalizeRecord(raw map[string]any) SheetRecord {
	record := SheetRecord{Fields: map[string]string{}}
	if id := cellText(raw["record_id"]); id != "" {
		record.ID = id
	} else if id := cellText(raw["id"]); id != "" {
		record.ID = id
	}

	values := raw
	for _, key := range []string{"values", "fields", "cell_values"} {
		if nested, ok := raw[key].(map[string]any); ok {
			values = nested
			break
		}
	}
	for key, value := range values {
		if key == "record_id" || key == "id" {
			continue
		}
		record.Fields[key] = cellText(value)
	}
	return record
}

func cellText(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(value)
	case bool:
		if value {
			return "true"
		}
		return "false"
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.10f", value), "0"), ".")
	case int:
		return fmt.Sprintf("%d", value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if text := cellText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ",")
	case map[string]any:
		for _, key := range []string{"text", "value", "title", "name", "plain_text", "url"} {
			if text := cellText(value[key]); text != "" {
				return text
			}
		}
		for _, key := range []string{"values", "cell_value", "elements"} {
			if text := cellText(value[key]); text != "" {
				return text
			}
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			if text := cellText(value[key]); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ",")
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}
