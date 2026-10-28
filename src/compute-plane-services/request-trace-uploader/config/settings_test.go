// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsForPipelineSettings(t *testing.T) {
	cfg, warnings, err := Load(mapLookup(requiredEnv(nil)))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load() error = %v warnings = %v", err, warnings)
	}
	if cfg.AuditPrefix != "" || cfg.SkipRedaction || cfg.DrainTimeout != DefaultDrainTimeout || cfg.MaxJobAge != DefaultMaxJobAge {
		t.Fatalf("got audit %q skip redaction %v drain %v max job age %v", cfg.AuditPrefix, cfg.SkipRedaction, cfg.DrainTimeout, cfg.MaxJobAge)
	}
	if DefaultDrainTimeout != 60*time.Second {
		t.Fatalf("DefaultDrainTimeout = %v, want the shell exporter's 60 s", DefaultDrainTimeout)
	}
	if got := cfg.Prefixes(); !slices.Equal(got, []string{DefaultSegmentPrefix}) {
		t.Fatalf("Prefixes() = %v, want only the segment prefix", got)
	}
}

func TestLoadParsesPipelineSettings(t *testing.T) {
	cfg, warnings, err := Load(mapLookup(requiredEnv(map[string]string{
		EnvAuditPrefix:      "request-audit",
		EnvRedactMultimodal: "False",
		EnvDrainTimeout:     "15",
		EnvMaxJobAge:        "21600",
	})))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load() error = %v warnings = %v", err, warnings)
	}
	if cfg.AuditPrefix != "request-audit" || !cfg.SkipRedaction || cfg.DrainTimeout != 15*time.Second || cfg.MaxJobAge != 6*time.Hour {
		t.Fatalf("got audit %q skip redaction %v drain %v max job age %v", cfg.AuditPrefix, cfg.SkipRedaction, cfg.DrainTimeout, cfg.MaxJobAge)
	}
	if got := cfg.Prefixes(); !slices.Equal(got, []string{DefaultSegmentPrefix, "request-audit"}) {
		t.Fatalf("Prefixes() = %v, want both families", got)
	}
	cfg, _, err = Load(mapLookup(requiredEnv(map[string]string{EnvDrainTimeout: "0"})))
	if err != nil || cfg.DrainTimeout != 0 {
		t.Fatalf("drain 0: error = %v drain = %v, want no drain wait", err, cfg.DrainTimeout)
	}
}

func TestLoadRejectsInvalidPipelineSettings(t *testing.T) {
	tests := map[string]map[string]string{
		"audit prefix with a separator":  {EnvAuditPrefix: "audit/x"},
		"audit prefix equals the prefix": {EnvAuditPrefix: DefaultSegmentPrefix},
		"audit prefix extends the other": {EnvAuditPrefix: DefaultSegmentPrefix + ".audit"},
		"redaction not a boolean":        {EnvRedactMultimodal: "sometimes"},
		"drain negative":                 {EnvDrainTimeout: "-1"},
		"drain not a number":             {EnvDrainTimeout: "1m"},
		"drain too long":                 {EnvDrainTimeout: "3601"},
		"max job age zero":               {EnvMaxJobAge: "0"},
		"max job age not a number":       {EnvMaxJobAge: "6h"},
	}
	for name, env := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Load(mapLookup(requiredEnv(env))); err == nil {
				t.Fatal("Load() error = nil, want a startup error")
			}
		})
	}
}

func mapLookup(values map[string]string) LookupFunc {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

// bashNames are the names the Bash uploader's chart sets for settings Go also
// has. Go reads only its own names, so none of them may change a setting.
var bashNames = map[string]string{
	"TRACE_DIR":               "/bash-spool",
	"TRACE_FILE_PREFIX":       "bash-trace",
	"UPLOAD_INTERVAL_SECONDS": "60",
	"SHUTDOWN_DRAIN_SECONDS":  "15",
}

func TestLoadStrictIgnoresBashEnvironmentNames(t *testing.T) {
	env := requiredEnv(nil)
	environ := []string{EnvSourceDir + "=" + env[EnvSourceDir], EnvBackend + "=" + env[EnvBackend]}
	for name, value := range bashNames {
		env[name] = value
		environ = append(environ, name+"="+value)
	}
	cfg, err := LoadStrict(mapLookup(env), environ)
	if err != nil {
		t.Fatalf("LoadStrict() error = %v", err)
	}
	if cfg.SourceDir != env[EnvSourceDir] || cfg.SegmentPrefix != DefaultSegmentPrefix ||
		cfg.ScanInterval != DefaultScanInterval || cfg.DrainTimeout != DefaultDrainTimeout {
		t.Fatalf("got source %q prefix %q scan %v drain %v, want the Go settings and defaults only",
			cfg.SourceDir, cfg.SegmentPrefix, cfg.ScanInterval, cfg.DrainTimeout)
	}
}

func TestLoadStrictNeedsTheGoSourceDirEvenWhenTheBashOneIsSet(t *testing.T) {
	env := map[string]string{EnvBackend: string(BackendDebug), "TRACE_DIR": "/bash-spool"}
	_, err := LoadStrict(mapLookup(env), []string{"TRACE_DIR=/bash-spool"})
	if err == nil || !strings.Contains(err.Error(), EnvSourceDir) {
		t.Fatalf("LoadStrict() error = %v, want %s required", err, EnvSourceDir)
	}
}

func TestLoadStrictTurnsEveryFallbackIntoAnError(t *testing.T) {
	_, err := LoadStrict(mapLookup(requiredEnv(map[string]string{EnvScanInterval: "soon"})), nil)
	if err == nil || !strings.Contains(err.Error(), EnvScanInterval) {
		t.Fatalf("LoadStrict() error = %v, want it to name %s", err, EnvScanInterval)
	}
	if strings.Contains(err.Error(), "soon") {
		t.Fatalf("LoadStrict() error = %q, echoes the value", err)
	}
}

func TestLoadStrictRejectsUnknownSettings(t *testing.T) {
	environ := []string{"PATH=/bin", "OTHER_SERVICE_SETTING=x", EnvSourceDir + "=/records", "REQUEST_TRACE_UPLOADER_SCAN_INTERVAL=30"}
	_, err := LoadStrict(mapLookup(requiredEnv(nil)), environ)
	if err == nil || !strings.Contains(err.Error(), "REQUEST_TRACE_UPLOADER_SCAN_INTERVAL") {
		t.Fatalf("LoadStrict() error = %v, want the misspelled setting named", err)
	}
}
