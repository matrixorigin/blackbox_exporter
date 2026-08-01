// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package config

import (
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestLoadJustMySocksModule(t *testing.T) {
	path := writeConfigFile(t, `
modules:
  shanghai_vpn_traffic:
    prober: justmysocks
    timeout: 30s
    justmysocks:
      url_file: /etc/blackbox-secrets/justmysocks-url
      refresh_interval: 1h
`)
	sc := NewSafeConfig(prometheus.NewRegistry())
	if err := sc.ReloadConfig(path, nil); err != nil {
		t.Fatal(err)
	}
	module := sc.C.Modules["shanghai_vpn_traffic"]
	if module.JustMySocks.URLFile != "/etc/blackbox-secrets/justmysocks-url" {
		t.Fatalf("url_file = %q", module.JustMySocks.URLFile)
	}
	if module.JustMySocks.RefreshInterval != time.Hour {
		t.Fatalf("refresh_interval = %s, want 1h", module.JustMySocks.RefreshInterval)
	}
}

func TestJustMySocksModuleRequiresURLFile(t *testing.T) {
	path := writeConfigFile(t, `
modules:
  shanghai_vpn_traffic:
    prober: justmysocks
`)
	sc := NewSafeConfig(prometheus.NewRegistry())
	err := sc.ReloadConfig(path, nil)
	if err == nil || err.Error() != "error parsing config file: url_file must be set for Just My Socks module" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestJustMySocksModuleDefaultsToHourlyRefresh(t *testing.T) {
	path := writeConfigFile(t, `
modules:
  shanghai_vpn_traffic:
    prober: justmysocks
    justmysocks:
      url_file: /etc/blackbox-secrets/justmysocks-url
`)
	sc := NewSafeConfig(prometheus.NewRegistry())
	if err := sc.ReloadConfig(path, nil); err != nil {
		t.Fatal(err)
	}
	if got := sc.C.Modules["shanghai_vpn_traffic"].JustMySocks.RefreshInterval; got != time.Hour {
		t.Fatalf("refresh_interval = %s, want 1h", got)
	}
}

func TestJustMySocksModuleRejectsNonPositiveRefresh(t *testing.T) {
	path := writeConfigFile(t, `
modules:
  shanghai_vpn_traffic:
    prober: justmysocks
    justmysocks:
      url_file: /etc/blackbox-secrets/justmysocks-url
      refresh_interval: 0s
`)
	sc := NewSafeConfig(prometheus.NewRegistry())
	err := sc.ReloadConfig(path, nil)
	if err == nil || err.Error() != "error parsing config file: refresh_interval must be positive for Just My Socks module" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeConfigFile(t *testing.T, contents string) string {
	t.Helper()
	path := t.TempDir() + "/blackbox.yml"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
