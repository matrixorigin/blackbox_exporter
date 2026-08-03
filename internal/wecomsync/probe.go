// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package wecomsync

import (
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const managedByLabel = "app.kubernetes.io/managed-by"

type ProbeSpec struct {
	Name          string
	JobName       string
	Module        string
	Target        string
	Interval      string
	ScrapeTimeout string
	Labels        map[string]string
}

func RecordsToProbes(records []SheetRecord, cfg Config) ([]ProbeSpec, error) {
	allowed := map[string]struct{}{}
	for _, module := range cfg.AllowedModules {
		allowed[module] = struct{}{}
	}

	var probes []ProbeSpec
	seen := map[string]struct{}{}
	for _, record := range records {
		if !recordEnabled(record, cfg.Columns.Enabled) {
			continue
		}
		probe, err := recordToProbe(record, cfg, allowed)
		if err != nil {
			continue
		}
		if _, ok := seen[probe.Name]; ok {
			continue
		}
		seen[probe.Name] = struct{}{}
		probes = append(probes, probe)
	}

	sort.Slice(probes, func(i, j int) bool { return probes[i].Name < probes[j].Name })
	return probes, nil
}

func recordEnabled(record SheetRecord, enabledColumn string) bool {
	value := strings.ToLower(strings.TrimSpace(record.Fields[enabledColumn]))
	switch value {
	case "true", "yes", "y", "1", "enabled", "enable", "启用", "是":
		return true
	default:
		return false
	}
}

func recordToProbe(record SheetRecord, cfg Config, allowed map[string]struct{}) (ProbeSpec, error) {
	target := strings.TrimSpace(record.Fields[cfg.Columns.Target])
	if target == "" {
		return ProbeSpec{}, fmt.Errorf("record %q target is empty", record.ID)
	}

	module := strings.TrimSpace(record.Fields[cfg.Columns.Module])
	if module == "" {
		module = cfg.Defaults.Module
	}
	if _, ok := allowed[module]; !ok {
		return ProbeSpec{}, fmt.Errorf("record %q uses unsupported module %q", record.ID, module)
	}

	interval := valueOrDefault(record.Fields[cfg.Columns.Interval], cfg.Defaults.Interval)
	if _, err := time.ParseDuration(interval); err != nil {
		return ProbeSpec{}, fmt.Errorf("record %q interval %q is invalid: %w", record.ID, interval, err)
	}
	scrapeTimeout := valueOrDefault(record.Fields[cfg.Columns.ScrapeTimeout], cfg.Defaults.ScrapeTimeout)
	if _, err := time.ParseDuration(scrapeTimeout); err != nil {
		return ProbeSpec{}, fmt.Errorf("record %q scrape_timeout %q is invalid: %w", record.ID, scrapeTimeout, err)
	}

	nameSource := strings.TrimSpace(record.Fields[cfg.Columns.Name])
	if nameSource == "" {
		nameSource = target
	}
	namePrefix := cfg.Kubernetes.NamePrefix
	if len(cfg.WeComSheetIDs()) > 1 && strings.TrimSpace(record.SheetID) != "" {
		namePrefix = strings.TrimSuffix(namePrefix, "-") + "-" + record.SheetID
	}
	name := resourceName(namePrefix, nameSource, target)
	jobName := strings.TrimSpace(record.Fields[cfg.Columns.JobName])
	if jobName == "" {
		jobName = name
	}

	labels, err := labelsForRecord(record, cfg)
	if err != nil {
		return ProbeSpec{}, err
	}
	return ProbeSpec{
		Name:          name,
		JobName:       jobName,
		Module:        module,
		Target:        target,
		Interval:      interval,
		ScrapeTimeout: scrapeTimeout,
		Labels:        labels,
	}, nil
}

func labelsForRecord(record SheetRecord, cfg Config) (map[string]string, error) {
	labels := map[string]string{
		"source": "wecom-smartsheet",
	}
	for key, value := range cfg.DefaultLabels {
		if strings.TrimSpace(value) != "" {
			labels[key] = strings.TrimSpace(value)
		}
	}
	for _, column := range cfg.Columns.LabelColumns {
		value := strings.TrimSpace(record.Fields[column])
		if value != "" {
			labels[sanitizeLabelName(column)] = value
		}
	}
	if raw := strings.TrimSpace(record.Fields[cfg.Columns.Labels]); raw != "" {
		parsed, err := parseInlineLabels(raw)
		if err != nil {
			return nil, fmt.Errorf("record %q labels are invalid: %w", record.ID, err)
		}
		for key, value := range parsed {
			labels[key] = value
		}
	}
	return labels, nil
}

func parseInlineLabels(raw string) (map[string]string, error) {
	labels := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("%q is not key=value", part)
		}
		key = sanitizeLabelName(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			return nil, errors.New("label key and value must be non-empty")
		}
		labels[key] = value
	}
	return labels, nil
}

func ProbeObject(probe ProbeSpec, cfg Config) *unstructured.Unstructured {
	metadataLabels := map[string]any{
		managedByLabel:                cfg.Kubernetes.FieldManager,
		"app.kubernetes.io/component": "probe",
		"app.kubernetes.io/name":      probe.Name,
		"app.kubernetes.io/part-of":   "wecom-blackbox-monitoring",
		"release":                     cfg.Kubernetes.ReleaseLabel,
	}
	staticLabels := map[string]any{}
	for key, value := range probe.Labels {
		staticLabels[key] = value
	}

	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "monitoring.coreos.com/v1",
		"kind":       "Probe",
		"metadata": map[string]any{
			"name":      probe.Name,
			"namespace": cfg.Kubernetes.Namespace,
			"labels":    metadataLabels,
		},
		"spec": map[string]any{
			"interval": probe.Interval,
			"jobName":  probe.JobName,
			"module":   probe.Module,
			"prober": map[string]any{
				"path": cfg.Kubernetes.ProberPath,
				"url":  cfg.Kubernetes.ProberURL,
			},
			"scrapeTimeout": probe.ScrapeTimeout,
			"targets": map[string]any{
				"staticConfig": map[string]any{
					"labels": staticLabels,
					"static": []any{probe.Target},
				},
			},
		},
	}}
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

var invalidResourceNameChars = regexp.MustCompile(`[^a-z0-9-]+`)
var invalidLabelNameChars = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

func resourceName(prefix, value, fallback string) string {
	name := slugSource(value)
	name = strings.ReplaceAll(name, "_", "-")
	name = invalidResourceNameChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if name == "" {
		name = slugSource(fallback)
		name = strings.ReplaceAll(name, "_", "-")
		name = invalidResourceNameChars.ReplaceAllString(name, "-")
		name = strings.Trim(name, "-")
	}
	if name == "" {
		name = "target"
	}
	prefix = strings.Trim(invalidResourceNameChars.ReplaceAllString(strings.ToLower(prefix), "-"), "-")
	if prefix != "" {
		name = prefix + "-" + name
	}
	if len(name) > 63 {
		suffix := "-" + shortHash(value+"|"+fallback)
		name = strings.TrimRight(name[:63-len(suffix)], "-") + suffix
	}
	return name
}

func slugSource(value string) string {
	value = strings.TrimSpace(value)
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		if parsed.Path != "" && parsed.Path != "/" {
			return strings.ToLower(parsed.Host + "-" + strings.Trim(parsed.Path, "/"))
		}
		return strings.ToLower(parsed.Host)
	}
	return strings.ToLower(value)
}

func shortHash(value string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(value))
	return fmt.Sprintf("%08x", h.Sum32())
}

func sanitizeLabelName(name string) string {
	name = invalidLabelNameChars.ReplaceAllString(strings.TrimSpace(name), "_")
	name = strings.Trim(name, "_")
	if name == "" {
		return ""
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}
