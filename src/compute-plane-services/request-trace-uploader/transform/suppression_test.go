// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"reflect"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform/suppress"
)

func payloadLine(headers string, complete string) string {
	return `{"event_type":"request_payload","event_time_unix_ms":2,"payload":{"request_id":"r","http_request_headers":{` + headers +
		`},"request":{"messages":[]},"payload_complete":` + complete + `}}`
}

func namedSuppression() Config {
	cfg := DefaultConfig()
	cfg.Suppression = suppress.Config{Rules: []suppress.Rule{
		{Name: "tenants", Headers: []string{"x-tenant"}, Values: []string{"t1"}, Action: suppress.DropRecord},
		{Name: "nca_id", Headers: suppress.DefaultHeaders, Values: []string{"acme"}, Action: suppress.StripPayload},
	}}
	return cfg
}

func TestPrepareCountsEachNamedRule(t *testing.T) {
	result, _, _ := prepare(t, namedSuppression(), segmentFile(t,
		payloadLine(`"nvcf-ncaid":"acme"`, "true"),
		payloadLine(`"nvcf-ncaid":"other","x-tenant":"t1"`, "true"),
		payloadLine(`"x-tenant":"t2"`, "true"),
		payloadLine(`"x-other":"v"`, "true"),
	))
	want := map[string]RuleCount{
		"tenants": {HeaderPresent: 2, Matched: 1},
		"nca_id":  {HeaderPresent: 2, Matched: 1},
	}
	if !reflect.DeepEqual(result.Rules, want) {
		t.Fatalf("Rules = %+v, want %+v", result.Rules, want)
	}
}

func TestPrepareCountsIncompletePayloadsOnTheSourceRecord(t *testing.T) {
	// The suppression stub always says payload_complete false; only the
	// source record's own flag may count.
	result, _, _ := prepare(t, namedSuppression(), segmentFile(t,
		payloadLine(`"nvcf-ncaid":"acme"`, "true"),
		payloadLine(`"nvcf-ncaid":"other"`, "false"),
		payloadLine(`"nvcf-ncaid":"other"`, `"false"`),
		textLine,
	))
	if result.Incomplete != 1 {
		t.Fatalf("Incomplete = %d, want 1", result.Incomplete)
	}
}

func TestPrepareDropsHeaderlessRecordsByPolicyAndCountsThem(t *testing.T) {
	cfg := namedSuppression()
	cfg.Suppression.Headerless = suppress.HeaderlessDrop
	result, writer, _ := prepare(t, cfg, segmentFile(t,
		textLine,
		`{"event_type":"tool_end","tool":{"tool_call_id":"c"}}`,
		payloadLine(`"nvcf-ncaid":"other"`, "true"),
	))
	if result.Written != 1 || len(writer.payloads) != 1 {
		t.Fatalf("Written = %d, want only the payload record", result.Written)
	}
	want := map[string]int{"request_end": 1, "tool_end": 1}
	if !reflect.DeepEqual(result.HeaderlessDropped, want) || result.Dropped != 0 {
		t.Fatalf("HeaderlessDropped = %v Dropped = %d, want %v and no rule drop", result.HeaderlessDropped, result.Dropped, want)
	}
}

func TestPrepareNamesUnknownRecordTypesUnknown(t *testing.T) {
	cfg := namedSuppression()
	cfg.Suppression.Headerless = suppress.HeaderlessDrop
	result, _, _ := prepare(t, cfg, segmentFile(t, `{"event_type":"Some Future Type!"}`, `[1,2]`))
	if want := map[string]int{"unknown": 2}; !reflect.DeepEqual(result.HeaderlessDropped, want) {
		t.Fatalf("HeaderlessDropped = %v, want %v", result.HeaderlessDropped, want)
	}
}
