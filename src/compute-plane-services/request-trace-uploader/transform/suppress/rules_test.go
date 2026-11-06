// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package suppress

import (
	"slices"
	"testing"
)

func namedRules() Config {
	return Config{Rules: []Rule{
		{Name: "tenants", Headers: []string{"x-tenant"}, Values: []string{"t1"}, Action: DropRecord},
		{Name: "nca_id", Headers: DefaultHeaders, Values: []string{"acme"}, Action: StripPayload},
	}}
}

func TestOutcomeNamesTheMatchedRule(t *testing.T) {
	engine := mustEngine(t, namedRules())
	tests := []struct {
		name string
		line string
		rule string
	}{
		{name: "first rule", line: `{"event_type":"request_payload","payload":{"http_request_headers":{"x-tenant":"t1"}}}`, rule: "tenants"},
		{name: "second rule", line: `{"event_type":"request_payload","payload":{"http_request_headers":{"nvcf-ncaid":"acme"}}}`, rule: "nca_id"},
		{name: "no match", line: `{"event_type":"request_payload","payload":{"http_request_headers":{"nvcf-ncaid":"other"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, outcome := engine.Apply(mustDecode(t, tt.line)); outcome.Rule != tt.rule {
				t.Fatalf("outcome.Rule = %q, want %q", outcome.Rule, tt.rule)
			}
		})
	}
}

func TestOutcomeListsEveryRuleWhoseHeaderIsPresent(t *testing.T) {
	engine := mustEngine(t, namedRules())
	tests := []struct {
		name    string
		line    string
		present []string
	}{
		// Presence counts whether or not the value matched, and a match by
		// an earlier rule does not hide a later rule's header.
		{name: "both present, first matches", line: `{"event_type":"request_payload","payload":{"http_request_headers":{"x-tenant":"t1","nvcf-ncaid":"other"}}}`, present: []string{"tenants", "nca_id"}},
		{name: "one present, no match", line: `{"event_type":"request_payload","payload":{"http_request_headers":{"nvcf-nca-id":"other"}}}`, present: []string{"nca_id"}},
		{name: "blank value is absent", line: `{"event_type":"request_payload","payload":{"http_request_headers":{"nvcf-ncaid":" "}}}`},
		{name: "headers not captured", line: `{"event_type":"request_payload","payload":{"request_id":"r1"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, outcome := engine.Apply(mustDecode(t, tt.line))
			if !slices.Equal(outcome.Present, tt.present) {
				t.Fatalf("outcome.Present = %v, want %v", outcome.Present, tt.present)
			}
			if outcome.HeaderPresent != (len(tt.present) > 0) {
				t.Fatalf("outcome.HeaderPresent = %v, want %v", outcome.HeaderPresent, len(tt.present) > 0)
			}
		})
	}
}

func TestHeaderlessRecordsAreExportedByDefault(t *testing.T) {
	engine := mustEngine(t, namedRules())
	record := mustDecode(t, `{"event_type":"request_end","request":{"request_id":"r1"}}`)

	got, outcome := engine.Apply(record)
	if got != record || outcome.Action != "" || outcome.Headerless {
		t.Fatalf("got = %v outcome = %+v, want the record exported as is", got, outcome)
	}
}

func TestHeaderlessDropPolicyDropsRecordsThatCarryNoHeaders(t *testing.T) {
	cfg := namedRules()
	cfg.Headerless = HeaderlessDrop
	engine := mustEngine(t, cfg)

	got, outcome := engine.Apply(mustDecode(t, `{"event_type":"tool_end","tool":{"tool_call_id":"c1"}}`))
	if got != nil || outcome.Action != DropRecord || !outcome.Headerless || outcome.Rule != "" {
		t.Fatalf("got = %v outcome = %+v, want a headerless drop", got, outcome)
	}

	// A payload record with no captured header is still evaluated and kept:
	// a producer that does not capture the header must never cost records.
	payload := mustDecode(t, `{"event_type":"request_payload","payload":{"request_id":"r1"}}`)
	if got, outcome := engine.Apply(payload); got != payload || outcome.Action != "" || !outcome.Evaluated {
		t.Fatalf("got = %v outcome = %+v, want the payload record kept", got, outcome)
	}
}

func TestHeaderlessDropPolicyIsInertWithoutRules(t *testing.T) {
	engine := mustEngine(t, Config{Headerless: HeaderlessDrop})
	record := mustDecode(t, `{"event_type":"request_end"}`)
	if got, outcome := engine.Apply(record); got != record || outcome.Action != "" {
		t.Fatalf("got = %v outcome = %+v, want pass through when suppression is off", got, outcome)
	}
}

func TestConfigValidateRejectsBadNamesAndPolicies(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "duplicate rule name", cfg: Config{Rules: []Rule{
			{Name: "a", Headers: DefaultHeaders, Values: []string{"x"}, Action: StripPayload},
			{Name: "a", Headers: []string{"x-tenant"}, Values: []string{"y"}, Action: DropRecord},
		}}},
		{name: "unknown headerless policy", cfg: Config{Headerless: "reject"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want an error")
			}
		})
	}
	for _, policy := range []HeaderlessPolicy{"", HeaderlessExport, HeaderlessDrop} {
		if err := (Config{Headerless: policy}).Validate(); err != nil {
			t.Errorf("Validate(headerless %q) error = %v", policy, err)
		}
	}
}
