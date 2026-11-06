// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/transform/suppress"
)

func requiredEnv(extra map[string]string) map[string]string {
	env := map[string]string{EnvSourceDir: "/records", EnvBackend: string(BackendDebug)}
	for name, value := range extra {
		env[name] = value
	}
	return env
}

// chartNCAHeaders is the NCA id header list a deployment sets, in order.
const chartNCAHeaders = "nvcf-ncaid,nvcf-nca-id"

func ncaRule(values ...string) suppress.Rule {
	return suppress.Rule{Name: NCAIDRuleName, Headers: []string{"nvcf-ncaid", "nvcf-nca-id"}, Values: values, Action: suppress.StripPayload}
}

func TestLoadSuppressNCAIDs(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		rules []suppress.Rule
	}{
		{name: "unset is off"},
		{name: "empty is off", env: map[string]string{EnvSuppressNCAIDs: ""}},
		{name: "only separators is off", env: map[string]string{EnvSuppressNCAIDs: " , ,,"}},
		{name: "one id", env: map[string]string{EnvSuppressNCAIDs: "acme", EnvSuppressNCAHeaders: chartNCAHeaders}, rules: []suppress.Rule{ncaRule("acme")}},
		{
			name:  "split, trimmed and normalized like the shell exporter",
			env:   map[string]string{EnvSuppressNCAIDs: " nca-acme-nca , beta,,nca-gamma-nca,nca--nca", EnvSuppressNCAHeaders: chartNCAHeaders},
			rules: []suppress.Rule{ncaRule("acme", "beta", "gamma")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, warnings, err := Load(mapLookup(requiredEnv(tt.env)))
			if err != nil || len(warnings) != 0 {
				t.Fatalf("Load() error = %v warnings = %v", err, warnings)
			}
			if !reflect.DeepEqual(cfg.Suppression.Rules, tt.rules) {
				t.Fatalf("Rules = %+v, want %+v", cfg.Suppression.Rules, tt.rules)
			}
		})
	}
}

func TestLoadSuppressNCAHeaders(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		headers []string
		wantErr bool
	}{
		{name: "ids without headers fail", env: map[string]string{EnvSuppressNCAIDs: "acme"}, wantErr: true},
		{name: "ids with empty headers fail", env: map[string]string{EnvSuppressNCAIDs: "acme", EnvSuppressNCAHeaders: " "}, wantErr: true},
		{name: "no ids and no headers load with suppression off"},
		{name: "blank ids and no headers load with suppression off", env: map[string]string{EnvSuppressNCAIDs: " , "}},
		{name: "headers without ids load with suppression off", env: map[string]string{EnvSuppressNCAHeaders: "nvcf-ncaid"}},
		{
			name:    "headers are trimmed, lower-cased and kept in order",
			env:     map[string]string{EnvSuppressNCAIDs: "acme", EnvSuppressNCAHeaders: " X-Org-Id , nvcf-ncaid"},
			headers: []string{"x-org-id", "nvcf-ncaid"},
		},
		{name: "a blank header fails", env: map[string]string{EnvSuppressNCAIDs: "acme", EnvSuppressNCAHeaders: "x-org-id,,nvcf-ncaid"}, wantErr: true},
		{name: "a trailing separator fails", env: map[string]string{EnvSuppressNCAIDs: "acme", EnvSuppressNCAHeaders: "nvcf-ncaid,"}, wantErr: true},
		{name: "a repeated header fails", env: map[string]string{EnvSuppressNCAIDs: "acme", EnvSuppressNCAHeaders: "nvcf-ncaid, NVCF-NCAID"}, wantErr: true},
		{name: "a bad header list fails without ids", env: map[string]string{EnvSuppressNCAHeaders: "nvcf-ncaid,,x"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _, err := Load(mapLookup(requiredEnv(tt.env)))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load() error = nil, rules = %+v; want a startup error", cfg.Suppression.Rules)
				}
				if !strings.Contains(err.Error(), EnvSuppressNCAHeaders) {
					t.Fatalf("Load() error = %q, want it to name %s", err, EnvSuppressNCAHeaders)
				}
				if strings.Contains(err.Error(), "acme") {
					t.Fatalf("Load() error = %q, echoes a configured id", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if tt.headers == nil {
				if len(cfg.Suppression.Rules) != 0 {
					t.Fatalf("Rules = %+v, want suppression off", cfg.Suppression.Rules)
				}
				return
			}
			want := []suppress.Rule{{Name: NCAIDRuleName, Headers: tt.headers, Values: []string{"acme"}, Action: suppress.StripPayload}}
			if !reflect.DeepEqual(cfg.Suppression.Rules, want) {
				t.Fatalf("Rules = %+v, want %+v", cfg.Suppression.Rules, want)
			}
		})
	}
}

func TestLoadNamesBothSettingsWhenIDsHaveNoHeaders(t *testing.T) {
	_, _, err := Load(mapLookup(requiredEnv(map[string]string{EnvSuppressNCAIDs: "nca-acme-nca"})))
	if err == nil || !strings.Contains(err.Error(), EnvSuppressNCAIDs) || !strings.Contains(err.Error(), EnvSuppressNCAHeaders) {
		t.Fatalf("Load() error = %v, want one naming %s and %s", err, EnvSuppressNCAIDs, EnvSuppressNCAHeaders)
	}
	if strings.Contains(err.Error(), "acme") {
		t.Fatalf("Load() error = %q, echoes a configured id", err)
	}
}

func TestLoadParsesExplicitSuppressionRules(t *testing.T) {
	cfg, _, err := Load(mapLookup(requiredEnv(map[string]string{
		EnvSuppressNCAIDs:     "acme",
		EnvSuppressNCAHeaders: chartNCAHeaders,
		EnvSuppressionRules:   `[{"name":"tenants","headers":[" X-Tenant "],"values":["t1","t2"],"action":"dropRecord"}]`,
	})))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []suppress.Rule{
		ncaRule("acme"),
		{Name: "tenants", Headers: []string{"x-tenant"}, Values: []string{"t1", "t2"}, Action: suppress.DropRecord},
	}
	if !reflect.DeepEqual(cfg.Suppression.Rules, want) {
		t.Fatalf("Rules = %+v, want %+v", cfg.Suppression.Rules, want)
	}
}

func TestLoadHeaderlessPolicy(t *testing.T) {
	cfg, _, err := Load(mapLookup(requiredEnv(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Suppression.Headerless != suppress.HeaderlessExport {
		t.Fatalf("default Headerless = %q, want %q", cfg.Suppression.Headerless, suppress.HeaderlessExport)
	}
	cfg, _, err = Load(mapLookup(requiredEnv(map[string]string{EnvSuppressHeaderless: "drop"})))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Suppression.Headerless != suppress.HeaderlessDrop {
		t.Fatalf("Headerless = %q, want %q", cfg.Suppression.Headerless, suppress.HeaderlessDrop)
	}
}

func TestLoadRejectsInvalidSuppression(t *testing.T) {
	rule := func(body string) map[string]string {
		return map[string]string{EnvSuppressionRules: "[" + body + "]"}
	}
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "unparseable", env: map[string]string{EnvSuppressionRules: `[{"name":`}},
		{name: "not a list", env: map[string]string{EnvSuppressionRules: `{"name":"a"}`}},
		{name: "trailing data", env: map[string]string{EnvSuppressionRules: `[] []`}},
		{name: "unknown field", env: rule(`{"name":"a","header":"x-tenant","headers":["x-tenant"],"values":["t1"],"action":"dropRecord"}`)},
		{name: "unknown action", env: rule(`{"name":"a","headers":["x-tenant"],"values":["t1"],"action":"bashStub"}`)},
		{name: "missing action", env: rule(`{"name":"a","headers":["x-tenant"],"values":["t1"]}`)},
		{name: "empty value list", env: rule(`{"name":"a","headers":["x-tenant"],"values":[],"action":"dropRecord"}`)},
		{name: "only blank values", env: rule(`{"name":"a","headers":["x-tenant"],"values":[" ",""],"action":"dropRecord"}`)},
		{name: "no headers", env: rule(`{"name":"a","headers":[],"values":["t1"],"action":"dropRecord"}`)},
		{name: "blank header", env: rule(`{"name":"a","headers":[" "],"values":["t1"],"action":"dropRecord"}`)},
		{name: "missing name", env: rule(`{"headers":["x-tenant"],"values":["t1"],"action":"dropRecord"}`)},
		{name: "name is not a short lowercase token", env: rule(`{"name":"Tenant Rule","headers":["x-tenant"],"values":["t1"],"action":"dropRecord"}`)},
		{name: "duplicate names", env: rule(`{"name":"a","headers":["x-tenant"],"values":["t1"],"action":"dropRecord"},{"name":"a","headers":["x-org"],"values":["o1"],"action":"dropRecord"}`)},
		{name: "name taken by the nca id rule", env: map[string]string{
			EnvSuppressNCAIDs:     "acme",
			EnvSuppressNCAHeaders: chartNCAHeaders,
			EnvSuppressionRules:   `[{"name":"nca_id","headers":["x-tenant"],"values":["t1"],"action":"dropRecord"}]`,
		}},
		{name: "unknown headerless policy", env: map[string]string{EnvSuppressHeaderless: "reject"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Load(mapLookup(requiredEnv(tt.env)))
			if err == nil {
				t.Fatal("Load() error = nil, want a startup error")
			}
			// A configured value can be a customer identifier, so it must
			// not be echoed into the error.
			for _, value := range []string{"t1", "o1", "acme"} {
				if strings.Contains(err.Error(), value) {
					t.Fatalf("Load() error = %q, echoes a configured value", err)
				}
			}
		})
	}
}
