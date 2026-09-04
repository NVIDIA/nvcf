// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, warnings, err := Load(testLookup(map[string]string{
		EnvSourceDir: "/records",
		EnvBackend:   "objectstore",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if cfg.SegmentPrefix != DefaultSegmentPrefix {
		t.Fatalf("segment prefix = %q, want %q", cfg.SegmentPrefix, DefaultSegmentPrefix)
	}
	if cfg.ScanInterval != DefaultScanInterval {
		t.Fatalf("scan interval = %v, want %v", cfg.ScanInterval, DefaultScanInterval)
	}
	if cfg.Kratos.StatusInterval != DefaultStatusInterval || cfg.Kratos.StatusTimeout != DefaultStatusTimeout {
		t.Fatalf("unexpected kratos polling defaults: %+v", cfg.Kratos)
	}
	if cfg.RetryPolicy.AttemptTimeout != DefaultAttemptTimeout || cfg.RetryPolicy.OperationTimeout != DefaultOperationTimeout {
		t.Fatalf("unexpected retry defaults: %+v", cfg.RetryPolicy)
	}
	if cfg.StateDir != "/records/request-trace-uploader-state" || cfg.QuarantineDir != "/records/request-trace-uploader-quarantine" {
		t.Fatalf("unexpected derived directories: state=%q quarantine=%q", cfg.StateDir, cfg.QuarantineDir)
	}
}

func TestLoadAcceptsSupportedBackends(t *testing.T) {
	for _, backend := range []Backend{BackendObjectStore, BackendKratos} {
		t.Run(string(backend), func(t *testing.T) {
			cfg, _, err := Load(testLookup(map[string]string{
				EnvSourceDir: "/records",
				EnvBackend:   string(backend),
			}))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Backend != backend {
				t.Fatalf("backend = %q, want %q", cfg.Backend, backend)
			}
		})
	}
}

func TestLoadOverridesSegmentPrefix(t *testing.T) {
	cfg, _, err := Load(testLookup(map[string]string{
		EnvSourceDir:     "/records",
		EnvBackend:       "objectstore",
		EnvSegmentPrefix: "custom-prefix",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SegmentPrefix != "custom-prefix" {
		t.Fatalf("segment prefix = %q, want %q", cfg.SegmentPrefix, "custom-prefix")
	}
}

func TestLoadFallsBackForInvalidPolicy(t *testing.T) {
	cfg, warnings, err := Load(testLookup(map[string]string{
		EnvSourceDir:            "/records",
		EnvBackend:              "kratos",
		EnvAttemptTimeout:       "0s",
		EnvOperationTimeout:     "10s",
		EnvMaxRetries:           "99",
		EnvRetryInitialBackoff:  "not-a-duration",
		EnvRetryMaximumBackoff:  "1ms",
		EnvRetryMultiplier:      "nan",
		EnvKratosStatusTimeout:  "1",
		EnvKratosStatusInterval: "10",
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
	if cfg.Kratos.StatusTimeout != 10*time.Second {
		t.Errorf("status timeout = %v, want clamped %v", cfg.Kratos.StatusTimeout, 10*time.Second)
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
		{name: "missing directory", env: map[string]string{EnvBackend: "objectstore"}},
		{name: "relative directory", env: map[string]string{EnvSourceDir: "records", EnvBackend: "objectstore"}},
		{name: "missing backend", env: map[string]string{EnvSourceDir: "/records"}},
		{name: "unknown backend", env: map[string]string{EnvSourceDir: "/records", EnvBackend: "s3"}},
		{name: "prefix with separator", env: map[string]string{EnvSourceDir: "/records", EnvBackend: "objectstore", EnvSegmentPrefix: "nested/prefix"}},
		{name: "http objectstore endpoint", env: map[string]string{EnvSourceDir: "/records", EnvBackend: "objectstore", EnvObjectStoreEndpoint: "http://minio.internal:9000"}},
		{name: "schemeless objectstore endpoint", env: map[string]string{EnvSourceDir: "/records", EnvBackend: "objectstore", EnvObjectStoreEndpoint: "minio.internal:9000"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := Load(testLookup(tt.env)); err == nil {
				t.Fatal("Load() error = nil, want error")
			}
		})
	}
}

func TestLoadAcceptsAnHTTPSObjectStoreEndpoint(t *testing.T) {
	cfg, _, err := Load(testLookup(map[string]string{
		EnvSourceDir:           "/records",
		EnvBackend:             "objectstore",
		EnvObjectStoreEndpoint: "https://minio.internal:9000",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ObjectStore.Endpoint != "https://minio.internal:9000" {
		t.Fatalf("endpoint = %q, want the configured https endpoint", cfg.ObjectStore.Endpoint)
	}
}

func testLookup(values map[string]string) LookupFunc {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
