// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// isolateAliases gives a test an empty alias registry and restores the
// previous one afterwards.
func isolateAliases(t *testing.T) {
	t.Helper()
	aliasMu.Lock()
	saved := aliasesBySetting
	savedOwners := aliasOwner
	aliasesBySetting = map[string][]string{}
	aliasOwner = map[string]string{}
	aliasMu.Unlock()
	t.Cleanup(func() {
		aliasMu.Lock()
		aliasesBySetting = saved
		aliasOwner = savedOwners
		aliasMu.Unlock()
	})
}

func mapLookup(values map[string]string) LookupFunc {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func loadWithAliases(t *testing.T, env map[string]string) (Config, []string, error) {
	t.Helper()
	lookup, err := WithAliases(mapLookup(env))
	if err != nil {
		t.Fatalf("WithAliases() error = %v", err)
	}
	return Load(lookup)
}

func TestAliasSuppliesSetting(t *testing.T) {
	isolateAliases(t)
	RegisterEnvAlias(EnvSourceDir, "OLD_SOURCE_DIR")
	RegisterEnvAlias(EnvScanInterval, "OLD_SCAN_SECONDS")

	cfg, warnings, err := loadWithAliases(t, map[string]string{
		"OLD_SOURCE_DIR":   "/spool",
		"OLD_SCAN_SECONDS": "45",
		EnvBackend:         string(BackendDebug),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SourceDir != "/spool" {
		t.Fatalf("SourceDir = %q, want /spool from the alias", cfg.SourceDir)
	}
	if cfg.ScanInterval != 45*time.Second {
		t.Fatalf("ScanInterval = %v, want 45s from the alias", cfg.ScanInterval)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
}

func TestCanonicalNameWinsOverAlias(t *testing.T) {
	isolateAliases(t)
	RegisterEnvAlias(EnvSourceDir, "OLD_SOURCE_DIR")

	cfg, _, err := loadWithAliases(t, map[string]string{
		EnvSourceDir:     "/canonical",
		"OLD_SOURCE_DIR": "/alias",
		EnvBackend:       string(BackendDebug),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SourceDir != "/canonical" {
		t.Fatalf("SourceDir = %q, want /canonical", cfg.SourceDir)
	}
}

func TestBlankAliasValueIsUnset(t *testing.T) {
	isolateAliases(t)
	RegisterEnvAlias(EnvSegmentPrefix, "OLD_PREFIX")

	cfg, _, err := loadWithAliases(t, map[string]string{
		EnvSourceDir: "/spool",
		EnvBackend:   string(BackendDebug),
		"OLD_PREFIX": "  ",
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SegmentPrefix != DefaultSegmentPrefix {
		t.Fatalf("SegmentPrefix = %q, want default %q", cfg.SegmentPrefix, DefaultSegmentPrefix)
	}
}

func TestAliasValuesAreValidatedLikeCanonicalValues(t *testing.T) {
	isolateAliases(t)
	RegisterEnvAlias(EnvSourceDir, "OLD_SOURCE_DIR")
	RegisterEnvAlias(EnvScanInterval, "OLD_SCAN_SECONDS")

	_, _, err := loadWithAliases(t, map[string]string{
		"OLD_SOURCE_DIR": "relative/spool",
		EnvBackend:       string(BackendDebug),
	})
	if err == nil {
		t.Fatal("Load() error = nil for a relative source dir from an alias, want error")
	}

	_, warnings, err := loadWithAliases(t, map[string]string{
		EnvSourceDir:       "/spool",
		EnvBackend:         string(BackendDebug),
		"OLD_SCAN_SECONDS": "soon",
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if strings.Join(warnings, ",") != EnvScanInterval {
		t.Fatalf("warnings = %v, want [%s]", warnings, EnvScanInterval)
	}
}

func TestConflictingAliasesAreAnError(t *testing.T) {
	isolateAliases(t)
	RegisterEnvAlias(EnvSourceDir, "OLD_SOURCE_DIR")
	RegisterEnvAlias(EnvSourceDir, "OTHER_SOURCE_DIR")

	_, err := WithAliases(mapLookup(map[string]string{
		"OLD_SOURCE_DIR":   "/one",
		"OTHER_SOURCE_DIR": "/two",
	}))
	if err == nil {
		t.Fatal("WithAliases() error = nil, want a conflict error")
	}
	for _, name := range []string{"OLD_SOURCE_DIR", "OTHER_SOURCE_DIR", EnvSourceDir} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("WithAliases() error = %v, want it to name %s", err, name)
		}
	}

	if _, err := WithAliases(mapLookup(map[string]string{
		"OLD_SOURCE_DIR":   "/same",
		"OTHER_SOURCE_DIR": "/same",
	})); err != nil {
		t.Fatalf("WithAliases() error = %v for equal values, want nil", err)
	}

	if _, err := WithAliases(mapLookup(map[string]string{
		EnvSourceDir:       "/canonical",
		"OLD_SOURCE_DIR":   "/one",
		"OTHER_SOURCE_DIR": "/two",
	})); err != nil {
		t.Fatalf("WithAliases() error = %v when the canonical name is set, want nil", err)
	}
}

func TestWithAliasesRejectsNilLookup(t *testing.T) {
	if _, err := WithAliases(nil); err == nil {
		t.Fatal("WithAliases(nil) error = nil, want error")
	}
}

func TestRegisterEnvAliasPanicsOnWiringMistakes(t *testing.T) {
	tests := []struct {
		name    string
		setting string
		alias   string
	}{
		{"unknown setting", "REQUEST_TRACE_UPLOADER_NOT_A_SETTING", "OLD_NAME"},
		{"empty alias", EnvSourceDir, ""},
		{"alias with spaces", EnvSourceDir, "OLD NAME"},
		{"alias is a setting name", EnvSourceDir, EnvStateDir},
		{"alias registered twice", EnvStateDir, "TAKEN_NAME"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateAliases(t)
			RegisterEnvAlias(EnvSourceDir, "TAKEN_NAME")
			defer func() {
				if recover() == nil {
					t.Fatalf("RegisterEnvAlias(%q, %q) did not panic", tt.setting, tt.alias)
				}
			}()
			RegisterEnvAlias(tt.setting, tt.alias)
		})
	}
}

func TestEverySettingAcceptsAnAlias(t *testing.T) {
	isolateAliases(t)
	settings := []string{
		EnvSourceDir, EnvSegmentPrefix, EnvBackend, EnvSecretsFile, EnvStateDir,
		EnvQuarantineDir, EnvHealthAddr, EnvScanInterval, EnvKratosStatusInterval,
		EnvKratosStatusTimeout, EnvAttemptTimeout, EnvOperationTimeout, EnvMaxRetries,
		EnvRetryInitialBackoff, EnvRetryMaximumBackoff, EnvRetryMultiplier,
		EnvObjectStoreBucket, EnvObjectStoreRegion, EnvObjectStoreEndpoint,
		EnvObjectStoreKeyPrefix, EnvObjectStorePathStyle, EnvObjectStoreDryRun,
		EnvDebugVerbosity,
	}
	for i, setting := range settings {
		RegisterEnvAlias(setting, fmt.Sprintf("OLD_SETTING_%d", i))
	}
}
