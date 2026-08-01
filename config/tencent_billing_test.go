// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");

package config

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestLoadTencentBillingModule(t *testing.T) {
	path := writeConfigFile(t, `
modules:
  tencent_cloud_balance:
    prober: tencent_billing
    timeout: 30s
    tencent_billing:
      refresh_interval: 15m
`)
	sc := NewSafeConfig(prometheus.NewRegistry())
	if err := sc.ReloadConfig(path, nil); err != nil {
		t.Fatal(err)
	}
	module := sc.C.Modules["tencent_cloud_balance"]
	if module.TencentBilling.RefreshInterval != 15*time.Minute {
		t.Fatalf("refresh_interval = %s, want 15m", module.TencentBilling.RefreshInterval)
	}
}

func TestTencentBillingModuleDefaultsToHourlyRefresh(t *testing.T) {
	path := writeConfigFile(t, `
modules:
  tencent_cloud_balance:
    prober: tencent_billing
`)
	sc := NewSafeConfig(prometheus.NewRegistry())
	if err := sc.ReloadConfig(path, nil); err != nil {
		t.Fatal(err)
	}
	module := sc.C.Modules["tencent_cloud_balance"]
	if module.TencentBilling.RefreshInterval != time.Hour {
		t.Fatalf("refresh_interval = %s, want 1h", module.TencentBilling.RefreshInterval)
	}
}

func TestTencentBillingModuleRejectsNonPositiveRefresh(t *testing.T) {
	path := writeConfigFile(t, `
modules:
  tencent_cloud_balance:
    prober: tencent_billing
    tencent_billing:
      refresh_interval: 0s
`)
	sc := NewSafeConfig(prometheus.NewRegistry())
	err := sc.ReloadConfig(path, nil)
	if err == nil || err.Error() != "error parsing config file: refresh_interval must be positive for Tencent billing module" {
		t.Fatalf("unexpected error: %v", err)
	}
}
