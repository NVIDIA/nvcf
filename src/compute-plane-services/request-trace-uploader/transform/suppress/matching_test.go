// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package suppress

import "testing"

// TestHeaderMatching pins how a header value is read and compared with a
// rule's values. A change to any row here changes which records are
// suppressed, so it must be deliberate.
func TestHeaderMatching(t *testing.T) {
	tests := []struct {
		name string
		// event is the JSON of a request_payload event.
		event string
		// headers is the rule's header group; empty means DefaultHeaders.
		headers []string
		values  []string
		// want is the matched id, or empty when the record is not matched.
		want string
	}{
		{name: "plain string", event: payloadHeaders(`"nvcf-ncaid":"acme"`), values: []string{"acme"}, want: "acme"},
		{name: "spaces and tabs are trimmed", event: payloadHeaders(`"nvcf-ncaid":" acme\t"`), values: []string{"acme"}, want: "acme"},
		{name: "separator controls are trimmed", event: payloadHeaders(`"nvcf-ncaid":"\u001cacme\u001f"`), values: []string{"acme"}, want: "acme"},
		{name: "header name ignores ASCII case", event: payloadHeaders(`"NVCF-NCAID":"acme"`), values: []string{"acme"}, want: "acme"},
		{name: "Kelvin sign folds to k", event: payloadHeaders(`"x-Key":"acme"`), headers: []string{"x-key"}, values: []string{"acme"}, want: "acme"},
		{name: "first key with the name decides", event: payloadHeaders(`"Nvcf-NcaId":"","nvcf-ncaid":"acme"`), values: []string{"acme"}},
		{name: "list gives its first non-empty item", event: payloadHeaders(`"nvcf-ncaid":["","acme"]`), values: []string{"acme"}, want: "acme"},
		{name: "list skips a zero", event: payloadHeaders(`"nvcf-ncaid":[0,"acme"]`), values: []string{"acme"}, want: "acme"},
		{name: "integer value", event: payloadHeaders(`"nvcf-ncaid":123`), values: []string{"123"}, want: "123"},
		{name: "float value", event: payloadHeaders(`"nvcf-ncaid":1.5`), values: []string{"1.5"}, want: "1.5"},
		{name: "float written with a trailing zero", event: payloadHeaders(`"nvcf-ncaid":1.50`), values: []string{"1.5"}, want: "1.5"},
		{name: "true is read as True", event: payloadHeaders(`"nvcf-ncaid":true`), values: []string{"True"}, want: "True"},
		{name: "zero falls through to the next name", event: payloadHeaders(`"nvcf-ncaid":0,"nvcf-nca-id":"acme"`), values: []string{"acme"}, want: "acme"},
		{name: "null falls through to the next name", event: payloadHeaders(`"nvcf-ncaid":null,"nvcf-nca-id":"acme"`), values: []string{"acme"}, want: "acme"},
		{name: "affixed id matches the bare id", event: payloadHeaders(`"nvcf-ncaid":"nca-acme-nca"`), values: []string{"acme"}, want: "acme"},
		{name: "one affix is kept", event: payloadHeaders(`"nvcf-ncaid":"nca-acme"`), values: []string{"acme"}},
		{
			name:   "blank top-level header falls through to payload headers",
			event:  `{"event_type":"request_payload","headers":{"nvcf-ncaid":"  "},"payload":{"http_request_headers":{"nvcf-ncaid":"acme"}}}`,
			values: []string{"acme"}, want: "acme",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := tt.headers
			if len(headers) == 0 {
				headers = DefaultHeaders
			}
			engine := mustEngine(t, Config{Rules: []Rule{{Headers: headers, Values: tt.values, Action: DropRecord}}})
			_, outcome := engine.Apply(mustDecode(t, tt.event))
			if outcome.ID != tt.want || (tt.want != "") != (outcome.Action == DropRecord) {
				t.Fatalf("outcome = %+v, want matched id %q", outcome, tt.want)
			}
		})
	}
}

func payloadHeaders(members string) string {
	return `{"event_type":"request_payload","payload":{"http_request_headers":{` + members + `}}}`
}
