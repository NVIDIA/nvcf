// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"reflect"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, warnings, err := Load(testLookup(map[string]string{
		EnvTraceDir:        "/records",
		EnvTraceFilePrefix: "request-trace",
		EnvAuditFilePrefix: "request-audit",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if cfg.UploadInterval != DefaultUploadInterval || cfg.StatusInterval != DefaultStatusInterval || cfg.StatusTimeout != DefaultStatusTimeout {
		t.Fatalf("unexpected polling defaults: %+v", cfg)
	}
	if cfg.RetryPolicy.AttemptTimeout != DefaultAttemptTimeout || cfg.RetryPolicy.OperationTimeout != DefaultOperationTimeout {
		t.Fatalf("unexpected retry defaults: %+v", cfg.RetryPolicy)
	}
	if cfg.StateDir != "/records/request-trace-uploader-state" || cfg.QuarantineDir != "/records/request-trace-uploader-quarantine" {
		t.Fatalf("unexpected derived directories: state=%q quarantine=%q", cfg.StateDir, cfg.QuarantineDir)
	}
}

func TestLoadNormalizesDroppedNCAIDs(t *testing.T) {
	cfg, warnings, err := Load(testLookup(map[string]string{
		EnvTraceDir:        "/records",
		EnvTraceFilePrefix: "request-trace",
		EnvAuditFilePrefix: "request-audit",
		EnvDroppedNCAIDs:   "  first, nca-second-nca, first, , third ",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(cfg.DroppedNCAIDs, want) {
		t.Errorf("DroppedNCAIDs = %v, want %v", cfg.DroppedNCAIDs, want)
	}
}

func TestLoadRejectsInvalidDroppedNCAID(t *testing.T) {
	_, _, err := Load(testLookup(map[string]string{
		EnvTraceDir:        "/records",
		EnvTraceFilePrefix: "request-trace",
		EnvAuditFilePrefix: "request-audit",
		EnvDroppedNCAIDs:   "nca--nca",
	}))
	if err == nil {
		t.Fatal("Load() error = nil, want invalid NCA ID error")
	}
}

func TestConfigDropsNCAID(t *testing.T) {
	cfg, _, err := Load(testLookup(map[string]string{
		EnvTraceDir:        "/records",
		EnvTraceFilePrefix: "request-trace",
		EnvAuditFilePrefix: "request-audit",
		EnvDroppedNCAIDs:   "customer",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.DropsNCAID("nca-customer-nca") {
		t.Error("DropsNCAID(wrapper) = false, want true")
	}
	if cfg.DropsNCAID("CUSTOMER") {
		t.Error("DropsNCAID(case changed) = true, want false")
	}
	if cfg.DropsNCAID("") {
		t.Error("DropsNCAID(empty) = true, want false")
	}
}

func TestLoadFallsBackForInvalidPolicy(t *testing.T) {
	cfg, warnings, err := Load(testLookup(map[string]string{
		EnvTraceDir:            "/records",
		EnvTraceFilePrefix:     "request-trace",
		EnvAuditFilePrefix:     "request-audit",
		EnvAttemptTimeout:      "0s",
		EnvOperationTimeout:    "10s",
		EnvMaxRetries:          "99",
		EnvRetryInitialBackoff: "not-a-duration",
		EnvRetryMaximumBackoff: "1ms",
		EnvRetryMultiplier:     "nan",
		EnvStatusTimeout:       "1",
		EnvStatusInterval:      "10",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.RetryPolicy.AttemptTimeout != DefaultAttemptTimeout {
		t.Errorf("attempt timeout = %v, want %v", cfg.RetryPolicy.AttemptTimeout, DefaultAttemptTimeout)
	}
	if cfg.RetryPolicy.MaxRetries != DefaultMaxRetries {
		t.Errorf("max retries = %d, want %d", cfg.RetryPolicy.MaxRetries, DefaultMaxRetries)
	}
	if cfg.StatusTimeout != 10*time.Second {
		t.Errorf("status timeout = %v, want clamped %v", cfg.StatusTimeout, 10*time.Second)
	}
	if len(warnings) < 6 {
		t.Errorf("warnings = %v, want policy fallbacks", warnings)
	}
}

func TestLoadRejectsInvalidRequiredValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "missing directory", env: map[string]string{EnvTraceFilePrefix: "trace", EnvAuditFilePrefix: "audit"}},
		{name: "relative directory", env: map[string]string{EnvTraceDir: "records", EnvTraceFilePrefix: "trace", EnvAuditFilePrefix: "audit"}},
		{name: "same prefix", env: map[string]string{EnvTraceDir: "/records", EnvTraceFilePrefix: "trace", EnvAuditFilePrefix: "trace"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := Load(testLookup(tt.env)); err == nil {
				t.Fatal("Load() error = nil, want error")
			}
		})
	}
}

func testLookup(values map[string]string) LookupFunc {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
