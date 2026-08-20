// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
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
