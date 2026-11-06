// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package suppress

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/transform/jsonvalue"
)

// ncaHeaders is the header list the earlier Python uploader read the NCA id
// from, in its order. The reference cases in testdata/python.json assume it.
var ncaHeaders = []string{"nvcf-ncaid", "nvcf-nca-id"}

// goldenCase holds what the earlier Python uploader produced for one line:
// the NCA id it resolved and, when suppressed, the stub it wrote.
type goldenCase struct {
	In    string  `json:"in"`
	IDs   string  `json:"ids"`
	NCAID string  `json:"nca_id"`
	Out   *string `json:"out"`
}

func loadGolden(t *testing.T) []goldenCase {
	t.Helper()
	data, err := os.ReadFile("testdata/python.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func mustDecode(t *testing.T, line string) *jsonvalue.Value {
	t.Helper()
	value, err := jsonvalue.Decode([]byte(line))
	if err != nil {
		t.Fatalf("Decode(%s) error = %v", line, err)
	}
	return value
}

func mustEngine(t *testing.T, cfg Config) *Engine {
	t.Helper()
	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return engine
}

// plainValueChanges lists the reference cases, by request id, where reading
// header values as plain values resolves a different id than the earlier
// Python uploader did: a boolean is no longer read as "True", and a zero is
// a value instead of falling through to the next header.
var plainValueChanges = map[string]string{
	`"request_id":"r22"`: "",
	`"request_id":"r23"`: "0",
}

func plainValueChange(line string) (string, bool) {
	for key, id := range plainValueChanges {
		if strings.Contains(line, key) {
			return id, true
		}
	}
	return "", false
}

// TestStubMatchesPythonUploader checks that the stub has the earlier Python
// uploader's keys, key order and values. Its bytes are plain JSON, so
// formatting such as non-ASCII escapes may differ.
func TestStubMatchesPythonUploader(t *testing.T) {
	for _, tc := range loadGolden(t) {
		t.Run(tc.In, func(t *testing.T) {
			engine := mustEngine(t, FromIDs(strings.Split(tc.IDs, ","), ncaHeaders...))
			record := mustDecode(t, tc.In)
			before := string(jsonvalue.Encode(record))

			got, outcome := engine.Apply(record)
			if _, changed := plainValueChange(tc.In); changed {
				if outcome.Action != "" || got != record {
					t.Fatalf("outcome = %+v, want the record passed through", outcome)
				}
				return
			}
			if tc.Out == nil {
				if outcome.Action != "" || got != record {
					t.Fatalf("outcome = %+v, want the record passed through", outcome)
				}
				if after := string(jsonvalue.Encode(record)); after != before {
					t.Fatalf("record changed to %s", after)
				}
				return
			}
			if outcome.Action != StripPayload || outcome.ID != tc.NCAID {
				t.Fatalf("outcome = %+v, want stripPayload with id %q", outcome, tc.NCAID)
			}
			if want := mustDecode(t, *tc.Out); !jsonvalue.Equal(got, want) {
				t.Errorf("stub = %s\nwant   %s", jsonvalue.Encode(got), *tc.Out)
			}
		})
	}
}

func TestResolveIDMatchesPythonUploader(t *testing.T) {
	for _, tc := range loadGolden(t) {
		event := EventOf(mustDecode(t, tc.In))
		got := ""
		if CarriesPayload(event) {
			got = NormalizeID(ResolveHeader(HeaderSets(event), ncaHeaders...))
		}
		want := tc.NCAID
		if id, changed := plainValueChange(tc.In); changed {
			want = id
		}
		if got != want {
			t.Errorf("%s: id = %q, want %q", tc.In, got, want)
		}
	}
}

func TestDropRecordRuleDropsTheRecord(t *testing.T) {
	engine := mustEngine(t, Config{Rules: []Rule{{Headers: ncaHeaders, Values: []string{"acme"}, Action: DropRecord}}})
	record := mustDecode(t, `{"event_type":"request_payload","payload":{"http_request_headers":{"nvcf-ncaid":"nca-acme-nca"}}}`)

	got, outcome := engine.Apply(record)
	if got != nil || outcome.Action != DropRecord || outcome.ID != "acme" {
		t.Fatalf("got = %v outcome = %+v, want the record dropped for acme", got, outcome)
	}
}

func TestFirstMatchingRuleWins(t *testing.T) {
	engine := mustEngine(t, Config{Rules: []Rule{
		{Headers: []string{"x-tenant"}, Values: []string{"t1"}, Action: DropRecord},
		{Headers: ncaHeaders, Values: []string{"acme"}, Action: StripPayload},
	}})
	record := mustDecode(t, `{"event_type":"request_payload","payload":{"http_request_headers":{"nvcf-ncaid":"acme","x-tenant":"t1"}}}`)

	if _, outcome := engine.Apply(record); outcome.Action != DropRecord || outcome.ID != "t1" {
		t.Fatalf("outcome = %+v, want the first rule", outcome)
	}
}

func TestOutcomeReportsEvaluationAndHeaderPresence(t *testing.T) {
	engine := mustEngine(t, FromIDs([]string{"acme"}, ncaHeaders...))
	tests := []struct {
		name          string
		line          string
		evaluated     bool
		headerPresent bool
	}{
		{name: "payload with header", line: `{"event_type":"request_payload","payload":{"http_request_headers":{"nvcf-ncaid":"other"}}}`, evaluated: true, headerPresent: true},
		{name: "payload without header", line: `{"event_type":"request_payload","payload":{"http_request_headers":{"x":"y"}}}`, evaluated: true},
		{name: "trace record", line: `{"event_type":"request_end","headers":{"nvcf-ncaid":"acme"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, outcome := engine.Apply(mustDecode(t, tt.line))
			if outcome.Evaluated != tt.evaluated || outcome.HeaderPresent != tt.headerPresent || outcome.Action != "" {
				t.Fatalf("outcome = %+v, want evaluated %v header %v and no action", outcome, tt.evaluated, tt.headerPresent)
			}
		})
	}
}

func TestNoRulesPassesEverythingThrough(t *testing.T) {
	engine := mustEngine(t, FromIDs([]string{"", " "}))
	if engine.Active() {
		t.Fatal("Active() = true with only blank ids")
	}
	record := mustDecode(t, `{"event_type":"request_payload","payload":{"http_request_headers":{"nvcf-ncaid":""}}}`)
	if got, outcome := engine.Apply(record); got != record || outcome.Action != "" {
		t.Fatalf("outcome = %+v, want pass through", outcome)
	}
}

func TestNormalizeID(t *testing.T) {
	tests := map[string]string{
		"acme":           "acme",
		"nca-acme-nca":   "acme",
		" nca-acme-nca ": "acme",
		"nca-acme":       "nca-acme",
		"acme-nca":       "acme-nca",
		"nca--nca":       "",
		"":               "",
	}
	for in, want := range tests {
		if got := NormalizeID(in); got != want {
			t.Errorf("NormalizeID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{name: "none", cfg: Config{}},
		{name: "from ids", cfg: FromIDs([]string{"a", "b"}, ncaHeaders...)},
		{name: "unknown action", cfg: Config{Rules: []Rule{{Headers: ncaHeaders, Values: []string{"a"}, Action: "bashStub"}}}, wantErr: true},
		{name: "no headers", cfg: Config{Rules: []Rule{{Values: []string{"a"}, Action: StripPayload}}}, wantErr: true},
		{name: "blank header", cfg: Config{Rules: []Rule{{Headers: []string{""}, Values: []string{"a"}, Action: StripPayload}}}, wantErr: true},
		{name: "upper-case header", cfg: Config{Rules: []Rule{{Headers: []string{"NVCF-NCAID"}, Values: []string{"a"}, Action: StripPayload}}}, wantErr: true},
		{name: "no values", cfg: Config{Rules: []Rule{{Headers: ncaHeaders, Values: []string{" "}, Action: StripPayload}}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if _, err := New(tt.cfg); (err != nil) != tt.wantErr {
				t.Fatalf("New() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestStubIsPlainJSON(t *testing.T) {
	engine := mustEngine(t, FromIDs([]string{"acme"}, ncaHeaders...))
	record := mustDecode(t, "{\"timestamp\":1.5e3,\"event\":{\"event_type\":\"request_payload\",\"payload\":{\"model\":\"caf\u00e9 \u2603\",\"http_request_headers\":{\"nvcf-ncaid\":\"acme\"}}}}")
	got, _ := engine.Apply(record)
	want := "{\"timestamp\":1.5e3,\"event\":{\"event_type\":\"request_payload\",\"payload\":{\"model\":\"caf\u00e9 \u2603\",\"payload_complete\":false," +
		"\"payload_drop_reason\":\"nca_id\",\"payload_suppressed\":true,\"suppression_reason\":\"nca_id\",\"nca_id\":\"acme\",\"http_request_headers\":{\"nvcf-ncaid\":\"acme\"}}}}"
	if encoded := string(jsonvalue.Encode(got)); encoded != want {
		t.Fatalf("stub = %s\nwant   %s", encoded, want)
	}
}

func TestFromIDsReadsTheGivenHeadersInOrder(t *testing.T) {
	cfg := FromIDs([]string{"acme"}, "x-org-id", "nvcf-ncaid")
	if len(cfg.Rules) != 1 || !slices.Equal(cfg.Rules[0].Headers, []string{"x-org-id", "nvcf-ncaid"}) {
		t.Fatalf("rules = %+v, want one rule on x-org-id then nvcf-ncaid", cfg.Rules)
	}
	engine := mustEngine(t, cfg)
	tests := []struct {
		name    string
		headers string
		want    Action
	}{
		{name: "second header is read when the first is absent", headers: `"nvcf-ncaid":"nca-acme-nca"`, want: StripPayload},
		{name: "first header decides when present", headers: `"x-org-id":"other","nvcf-ncaid":"acme"`},
		{name: "a header outside the list is not read", headers: `"nvcf-nca-id":"acme"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, outcome := engine.Apply(mustDecode(t, payloadHeaders(tt.headers)))
			if outcome.Action != tt.want {
				t.Fatalf("outcome = %+v, want action %q", outcome, tt.want)
			}
		})
	}
}

func TestFromIDsHasNoBuiltInHeaders(t *testing.T) {
	if cfg := FromIDs(nil); len(cfg.Rules) != 0 {
		t.Fatalf("FromIDs(nil) = %+v, want suppression off", cfg)
	}
	cfg := FromIDs([]string{"acme"})
	if len(cfg.Rules) != 1 || len(cfg.Rules[0].Headers) != 0 {
		t.Fatalf("FromIDs without headers = %+v, want one rule with no header", cfg.Rules)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want a rule with no header rejected")
	}
}
