// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package wecomsync

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	defaultSyncInterval = time.Minute
	defaultNamespace    = "mo-ob"
	defaultFieldManager = "wecom-probe-syncer"
	defaultProberPath   = "/probe"
	defaultNamePrefix   = "wecom"
	defaultReleaseLabel = "mo-ob-opensource-tke"
	defaultPageSize     = 100
)

type Config struct {
	SyncInterval   time.Duration     `yaml:"sync_interval"`
	WeCom          WeComConfig       `yaml:"wecom"`
	Kubernetes     KubernetesConfig  `yaml:"kubernetes"`
	Defaults       ProbeDefaults     `yaml:"defaults"`
	Columns        ColumnConfig      `yaml:"columns"`
	DefaultLabels  map[string]string `yaml:"default_labels"`
	AllowedModules []string          `yaml:"allowed_modules"`
}

type WeComConfig struct {
	BaseURL        string `yaml:"base_url"`
	CorpID         string `yaml:"corpid"`
	CorpIDFile     string `yaml:"corpid_file"`
	CorpSecret     string `yaml:"corpsecret"`
	CorpSecretFile string `yaml:"corpsecret_file"`
	DocID          string `yaml:"docid"`
	SheetID        string `yaml:"sheet_id"`
	ViewID         string `yaml:"view_id"`
	KeyType        string `yaml:"key_type"`
	PageSize       int    `yaml:"page_size"`
}

type KubernetesConfig struct {
	Kubeconfig   string `yaml:"kubeconfig"`
	Namespace    string `yaml:"namespace"`
	FieldManager string `yaml:"field_manager"`
	ProberURL    string `yaml:"prober_url"`
	ProberPath   string `yaml:"prober_path"`
	NamePrefix   string `yaml:"name_prefix"`
	ReleaseLabel string `yaml:"release_label"`
	Prune        *bool  `yaml:"prune"`
}

type ProbeDefaults struct {
	Interval      string `yaml:"interval"`
	ScrapeTimeout string `yaml:"scrape_timeout"`
	Module        string `yaml:"module"`
}

type ColumnConfig struct {
	Enabled       string   `yaml:"enabled"`
	Name          string   `yaml:"name"`
	Module        string   `yaml:"module"`
	Target        string   `yaml:"target"`
	Interval      string   `yaml:"interval"`
	ScrapeTimeout string   `yaml:"scrape_timeout"`
	Labels        string   `yaml:"labels"`
	JobName       string   `yaml:"job_name"`
	LabelColumns  []string `yaml:"label_columns"`
}

func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	cfg = cfg.WithDefaults()
	return cfg, cfg.Validate()
}

func (c Config) WithDefaults() Config {
	if c.SyncInterval == 0 {
		c.SyncInterval = defaultSyncInterval
	}
	if c.WeCom.BaseURL == "" {
		c.WeCom.BaseURL = "https://qyapi.weixin.qq.com"
	}
	if c.WeCom.KeyType == "" {
		c.WeCom.KeyType = "CELL_VALUE_KEY_TYPE_FIELD_TITLE"
	}
	if c.WeCom.PageSize == 0 {
		c.WeCom.PageSize = defaultPageSize
	}
	if c.Kubernetes.Namespace == "" {
		c.Kubernetes.Namespace = defaultNamespace
	}
	if c.Kubernetes.FieldManager == "" {
		c.Kubernetes.FieldManager = defaultFieldManager
	}
	if c.Kubernetes.ProberPath == "" {
		c.Kubernetes.ProberPath = defaultProberPath
	}
	if c.Kubernetes.NamePrefix == "" {
		c.Kubernetes.NamePrefix = defaultNamePrefix
	}
	if c.Kubernetes.ReleaseLabel == "" {
		c.Kubernetes.ReleaseLabel = defaultReleaseLabel
	}
	if c.Defaults.Interval == "" {
		c.Defaults.Interval = "30s"
	}
	if c.Defaults.ScrapeTimeout == "" {
		c.Defaults.ScrapeTimeout = "10s"
	}
	if c.Defaults.Module == "" {
		c.Defaults.Module = "tcp_connect"
	}
	if c.Columns.Enabled == "" {
		c.Columns.Enabled = "enabled"
	}
	if c.Columns.Name == "" {
		c.Columns.Name = "name"
	}
	if c.Columns.Module == "" {
		c.Columns.Module = "module"
	}
	if c.Columns.Target == "" {
		c.Columns.Target = "target"
	}
	if c.Columns.Interval == "" {
		c.Columns.Interval = "interval"
	}
	if c.Columns.ScrapeTimeout == "" {
		c.Columns.ScrapeTimeout = "scrape_timeout"
	}
	if c.Columns.Labels == "" {
		c.Columns.Labels = "labels"
	}
	if c.Columns.JobName == "" {
		c.Columns.JobName = "job_name"
	}
	if c.DefaultLabels == nil {
		c.DefaultLabels = map[string]string{}
	}
	if c.AllowedModules == nil {
		c.AllowedModules = []string{"http_2xx", "http_2xx_3xx_no_redirect", "tcp_connect", "icmp", "ssh_banner"}
	}
	return c
}

func (c Config) Validate() error {
	var missing []string
	if strings.TrimSpace(c.WeCom.CorpID) == "" && strings.TrimSpace(c.WeCom.CorpIDFile) == "" {
		missing = append(missing, "wecom.corpid or wecom.corpid_file")
	}
	if strings.TrimSpace(c.WeCom.CorpSecret) == "" && strings.TrimSpace(c.WeCom.CorpSecretFile) == "" {
		missing = append(missing, "wecom.corpsecret or wecom.corpsecret_file")
	}
	if strings.TrimSpace(c.WeCom.DocID) == "" {
		missing = append(missing, "wecom.docid")
	}
	if strings.TrimSpace(c.WeCom.SheetID) == "" {
		missing = append(missing, "wecom.sheet_id")
	}
	if strings.TrimSpace(c.Kubernetes.ProberURL) == "" {
		missing = append(missing, "kubernetes.prober_url")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config: %s", strings.Join(missing, ", "))
	}
	if c.SyncInterval <= 0 {
		return errors.New("sync_interval must be positive")
	}
	if c.WeCom.PageSize <= 0 {
		return errors.New("wecom.page_size must be positive")
	}
	if _, err := time.ParseDuration(c.Defaults.Interval); err != nil {
		return fmt.Errorf("defaults.interval is invalid: %w", err)
	}
	if _, err := time.ParseDuration(c.Defaults.ScrapeTimeout); err != nil {
		return fmt.Errorf("defaults.scrape_timeout is invalid: %w", err)
	}
	return nil
}

func (c Config) WeComCredentials() (corpID, corpSecret string, err error) {
	corpID, err = readInlineOrFile(c.WeCom.CorpID, c.WeCom.CorpIDFile)
	if err != nil {
		return "", "", fmt.Errorf("read wecom corpid: %w", err)
	}
	corpSecret, err = readInlineOrFile(c.WeCom.CorpSecret, c.WeCom.CorpSecretFile)
	if err != nil {
		return "", "", fmt.Errorf("read wecom corpsecret: %w", err)
	}
	return corpID, corpSecret, nil
}

func readInlineOrFile(value, path string) (string, error) {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
