/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	openairesponses "github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/api/adapters/openairesponses"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/config"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/lastcluster"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
)

type lastClusterChainFixture struct {
	name, endpoint string
	turns          [3]string
}

func lastClusterChainFixtures() []lastClusterChainFixture {
	var fixtures []lastClusterChainFixture
	for _, tc := range []struct {
		name, endpoint, leading, initial string
		appended                         [2]string
	}{
		{
			name: "chat", endpoint: lastcluster.EndpointChatCompletions,
			initial: `{"role":"system","content":"Be concise"},{"role":"user","content":"hello"}`,
			appended: [2]string{
				`{"role":"assistant","content":"hi","extension":{"trace":"a1"}},{"role":"user","content":"next"}`,
				`{"role":"assistant","content":"again"},{"role":"user","content":"last"}`,
			},
		},
		{
			name: "chat/single_tool", endpoint: lastcluster.EndpointChatCompletions,
			initial: `{"role":"user","content":"hello"}`,
			appended: [2]string{
				`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"A\"}"}}]},{"role":"tool","tool_call_id":"c1","content":"sunny"}`,
				`{"role":"assistant","content":null,"tool_calls":[{"id":"c2","type":"function","function":{"name":"weather","arguments":"{\"city\":\"B\"}"}}]},{"role":"tool","tool_call_id":"c2","content":"rain"}`,
			},
		},
		{
			name: "chat/parallel_tools", endpoint: lastcluster.EndpointChatCompletions,
			initial: `{"role":"user","content":"hello"}`,
			appended: [2]string{
				`{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"weather","arguments":"{}"}},{"id":"c2","type":"function","function":{"name":"time","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"sunny"},{"role":"tool","tool_call_id":"c2","content":"noon"}`,
				`{"role":"assistant","tool_calls":[{"id":"c3","type":"function","function":{"name":"weather","arguments":"{}"}},{"id":"c4","type":"function","function":{"name":"time","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c3","content":"rain"},{"role":"tool","tool_call_id":"c4","content":"night"}`,
			},
		},
		{
			name: "responses/tools", endpoint: lastcluster.EndpointResponses,
			leading: `"instructions":"Be concise","prompt":{"id":"p1","variables":{"city":"A"}},`,
			initial: `{"role":"user","content":"hello"}`,
			appended: [2]string{
				`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"checking"}]},{"type":"reasoning","id":"r1","summary":[],"encrypted_content":"opaque1"},{"type":"function_call","call_id":"c1","name":"weather","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"sunny"}`,
				`{"type":"reasoning","id":"r2","summary":[]},{"type":"message","role":"assistant","content":"checking again"},{"type":"function_call","call_id":"c2","name":"weather","arguments":"{}"},{"type":"function_call_output","call_id":"c2","output":"rain"}`,
			},
		},
		{
			name: "responses/easy_messages", endpoint: lastcluster.EndpointResponses,
			initial: `{"role":"user","content":"hello"}`,
			appended: [2]string{
				`{"role":"assistant","content":"hi"},{"role":"user","content":"next"}`,
				`{"role":"assistant","content":"again"},{"role":"user","content":"last"}`,
			},
		},
		{
			name: "messages", endpoint: lastcluster.EndpointMessages,
			initial: `{"role":"user","content":"hello"}`,
			appended: [2]string{
				`{"role":"assistant","content":"hi"},{"role":"user","content":"next"}`,
				`{"role":"assistant","content":"again"},{"role":"user","content":"last"}`,
			},
		},
		{
			name: "messages/tools_thinking", endpoint: lastcluster.EndpointMessages,
			initial: `{"role":"user","content":"hello"}`,
			appended: [2]string{
				`{"role":"assistant","content":[{"type":"thinking","thinking":"check both","signature":"sig1"},{"type":"tool_use","id":"c1","name":"weather","input":{"city":"A"}},{"type":"tool_use","id":"c2","name":"time","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"sunny"},{"type":"tool_result","tool_use_id":"c2","content":"noon"}]}`,
				`{"role":"assistant","content":[{"type":"thinking","thinking":"check again","signature":"sig2"},{"type":"tool_use","id":"c3","name":"weather","input":{"city":"B"}},{"type":"tool_use","id":"c4","name":"time","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c3","content":"rain"},{"type":"tool_result","tool_use_id":"c4","content":"night"}]}`,
			},
		},
	} {
		field := "messages"
		if tc.endpoint == lastcluster.EndpointResponses {
			field = "input"
		}
		fixture := lastClusterChainFixture{name: tc.name, endpoint: tc.endpoint}
		items := tc.initial
		for i := range fixture.turns {
			if i > 0 {
				items += "," + tc.appended[i-1]
			}
			fixture.turns[i] = `{"model":"fn-alpha/company-name/model-name","max_tokens":64,` + tc.leading + `"` + field + `":[` + items + `]}`
		}
		fixtures = append(fixtures, fixture)
		if tc.name == "responses/easy_messages" {
			fixture.name = "responses/string_to_easy_messages"
			fixture.turns[0] = `{"model":"fn-alpha/company-name/model-name","input":"hello"}`
			fixtures = append(fixtures, fixture)
		}
		if tc.name == "messages/tools_thinking" {
			fixture.name = "messages/system_tools_thinking"
			for i := range fixture.turns {
				fixture.turns[i] = strings.Replace(fixture.turns[i], `"messages":`, `"system":[{"type":"text","text":"Be concise","cache_control":{"type":"ephemeral"}}],"messages":`, 1)
			}
			fixtures = append(fixtures, fixture)
		}
	}
	return fixtures
}

func deriveLastClusterFixture(t *testing.T, h *Handlers, reqCtx *requestctx.RequestContext, endpoint, body string) {
	t.Helper()
	switch endpoint {
	case lastcluster.EndpointChatCompletions:
		h.setChatLastClusterKeys(reqCtx, []byte(body))
	case lastcluster.EndpointResponses:
		var req openairesponses.CreateRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		h.setResponsesLastClusterKeys(reqCtx, &req, []byte(body))
	case lastcluster.EndpointMessages:
		var req struct {
			Messages []json.RawMessage `json:"messages"`
			System   json.RawMessage   `json:"system"`
		}
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		h.setMessagesLastClusterKeys(reqCtx, req.Messages, req.System)
	default:
		t.Fatalf("unknown endpoint %s", endpoint)
	}
}

func lastClusterPayloadContext() *requestctx.RequestContext {
	return &requestctx.RequestContext{
		SessionSource: requestctx.SessionSourcePayload, SessionID: "existing-session",
		CacheAffinityKey: "existing-affinity", OrgID: "org-1", ProjectID: "project-1",
	}
}

func enabledLastClusterHandlers() *Handlers {
	cfg := config.Default()
	cfg.Stargate.LastCluster.Enabled = true
	return &Handlers{config: cfg}
}

func TestPayloadLastClusterChains(t *testing.T) {
	for _, fixture := range lastClusterChainFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			h := enabledLastClusterHandlers()
			previousWrite := ""
			for i, body := range fixture.turns {
				reqCtx := lastClusterPayloadContext()
				deriveLastClusterFixture(t, h, reqCtx, fixture.endpoint, body)
				if reqCtx.LastClusterLookupKey != previousWrite {
					t.Fatalf("turn %d lookup = %q, previous write = %q", i+1, reqCtx.LastClusterLookupKey, previousWrite)
				}
				if !strings.HasPrefix(reqCtx.LastClusterWriteKey, "pc:v1:") || reqCtx.LastClusterWriteKey == previousWrite {
					t.Fatalf("turn %d write = %q, previous write = %q", i+1, reqCtx.LastClusterWriteKey, previousWrite)
				}
				if reqCtx.SessionID != "existing-session" || reqCtx.CacheAffinityKey != "existing-affinity" {
					t.Fatal("payload key derivation changed session or cache affinity")
				}
				previousWrite = reqCtx.LastClusterWriteKey
			}
		})
	}
}

func TestPayloadLastClusterHistoryChanges(t *testing.T) {
	for _, fixture := range lastClusterChainFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			h := enabledLastClusterHandlers()
			original := lastClusterPayloadContext()
			deriveLastClusterFixture(t, h, original, fixture.endpoint, fixture.turns[2])
			afterCut := strings.Replace(fixture.turns[2], `"last"`, `"changed"`, 1)
			if afterCut == fixture.turns[2] {
				afterCut = strings.Replace(fixture.turns[2], `"rain"`, `"changed"`, 1)
			}
			for _, tc := range []struct {
				name, body string
				sameLookup bool
			}{
				{"edited_user", strings.Replace(fixture.turns[2], `"hello"`, `"edited"`, 1), false},
				{"edited_assistant", strings.Replace(fixture.turns[2], `"assistant"`, `"user"`, 1), false},
				{"after_cut", afterCut, true},
			} {
				if tc.body == fixture.turns[2] {
					continue
				}
				t.Run(tc.name, func(t *testing.T) {
					edited := lastClusterPayloadContext()
					deriveLastClusterFixture(t, h, edited, fixture.endpoint, tc.body)
					if (edited.LastClusterLookupKey == original.LastClusterLookupKey) != tc.sameLookup {
						t.Fatalf("lookup equality = %v, want %v", edited.LastClusterLookupKey == original.LastClusterLookupKey, tc.sameLookup)
					}
					if edited.LastClusterWriteKey == original.LastClusterWriteKey {
						t.Fatal("changed history retained write value")
					}
				})
			}
			for i := 0; i < 2; i++ {
				first, other := lastClusterPayloadContext(), lastClusterPayloadContext()
				deriveLastClusterFixture(t, h, first, fixture.endpoint, fixture.turns[i])
				deriveLastClusterFixture(t, h, other, fixture.endpoint, strings.Replace(fixture.turns[i], `"hello"`, `"different first user"`, 1))
				if other.LastClusterWriteKey == first.LastClusterWriteKey || (i > 0 && other.LastClusterLookupKey == first.LastClusterLookupKey) {
					t.Fatal("different first user shared a value")
				}
			}
		})
	}
}

func TestPayloadLastClusterCanonicalJSON(t *testing.T) {
	for _, tc := range []struct{ endpoint, a, b string }{
		{lastcluster.EndpointChatCompletions,
			`{"messages":[{"role":"user","content":"hello","extension":{"a":1,"b":2}},{"role":"assistant","content":"hi"},{"role":"user","content":"next"}]}`,
			" { \"messages\" : [ {\"extension\":{\"b\":2,\"a\":1},\"content\":\"he\\u006clo\",\"role\":\"user\"}, {\"content\":\"hi\",\"role\":\"assistant\"}, {\"content\":\"next\",\"role\":\"user\"} ] } "},
		{lastcluster.EndpointResponses,
			`{"input":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"next"}],"instructions":"system","prompt":{"id":"p1","variables":{"a":1,"b":2}}}`,
			` { "prompt":{"variables":{"b":2,"a":1},"id":"p1"}, "instructions":"system", "input":[{"content":"hello","role":"user"},{"content":"hi","role":"assistant"},{"content":"next","role":"user"}] } `},
		{lastcluster.EndpointMessages,
			`{"system":[{"type":"text","text":"system"}],"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"next"}]}`,
			` { "messages":[{"content":"hello","role":"user"},{"content":"hi","role":"assistant"},{"content":"next","role":"user"}], "system":[{"text":"system","type":"text"}] } `},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			h := enabledLastClusterHandlers()
			a, b := lastClusterPayloadContext(), lastClusterPayloadContext()
			deriveLastClusterFixture(t, h, a, tc.endpoint, tc.a)
			deriveLastClusterFixture(t, h, b, tc.endpoint, tc.b)
			if a.LastClusterWriteKey == "" || a.LastClusterLookupKey != b.LastClusterLookupKey || a.LastClusterWriteKey != b.LastClusterWriteKey {
				t.Fatal("whitespace or object key order changed values")
			}
		})
	}
}

func TestPayloadLastClusterLeadingParts(t *testing.T) {
	for _, tc := range []struct{ endpoint, field, value string }{
		{lastcluster.EndpointMessages, "system", `"Be concise"`},
		{lastcluster.EndpointResponses, "instructions", `"Be concise"`},
		{lastcluster.EndpointResponses, "prompt", `{"id":"p1"}`},
	} {
		t.Run(tc.field, func(t *testing.T) {
			field := "messages"
			if tc.endpoint == lastcluster.EndpointResponses {
				field = "input"
			}
			body := `{"` + field + `":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"next"}]}`
			h := enabledLastClusterHandlers()
			absent, null, present := lastClusterPayloadContext(), lastClusterPayloadContext(), lastClusterPayloadContext()
			deriveLastClusterFixture(t, h, absent, tc.endpoint, body)
			deriveLastClusterFixture(t, h, null, tc.endpoint, `{"`+tc.field+`":null,`+body[1:])
			deriveLastClusterFixture(t, h, present, tc.endpoint, `{"`+tc.field+`":`+tc.value+`,`+body[1:])
			if absent.LastClusterWriteKey != null.LastClusterWriteKey || absent.LastClusterLookupKey != null.LastClusterLookupKey {
				t.Fatal("null leading part differs from absent")
			}
			if present.LastClusterWriteKey == absent.LastClusterWriteKey || present.LastClusterLookupKey == absent.LastClusterLookupKey {
				t.Fatal("leading part did not affect both values")
			}
		})
	}
}

func TestPayloadLastClusterIneligible(t *testing.T) {
	for _, fixture := range lastClusterChainFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			for _, source := range []string{requestctx.SessionSourceHeader, requestctx.SessionSourcePromptCacheKey, requestctx.SessionSourceConversationID, requestctx.SessionSourceClaudeCodeHeader, ""} {
				reqCtx := lastClusterPayloadContext()
				reqCtx.SessionSource = source
				deriveLastClusterFixture(t, enabledLastClusterHandlers(), reqCtx, fixture.endpoint, fixture.turns[1])
				if reqCtx.LastClusterLookupKey != "" || reqCtx.LastClusterWriteKey != "" {
					t.Fatalf("source %q received values", source)
				}
			}
			for _, h := range []*Handlers{nil, {}, {config: config.Default()}} {
				reqCtx := lastClusterPayloadContext()
				deriveLastClusterFixture(t, h, reqCtx, fixture.endpoint, fixture.turns[1])
				if reqCtx.LastClusterLookupKey != "" || reqCtx.LastClusterWriteKey != "" {
					t.Fatal("disabled handler computed values")
				}
			}
			deriveLastClusterFixture(t, enabledLastClusterHandlers(), nil, fixture.endpoint, fixture.turns[1])
		})
	}
	for _, body := range []string{
		`{"input":"hello","previous_response_id":"resp_1"}`,
		`{"input":[{"role":"user","content":"hello"},{"type":"item_reference","id":"msg_1"}]}`,
	} {
		reqCtx := lastClusterPayloadContext()
		deriveLastClusterFixture(t, enabledLastClusterHandlers(), reqCtx, lastcluster.EndpointResponses, body)
		if reqCtx.LastClusterLookupKey != "" || reqCtx.LastClusterWriteKey != "" {
			t.Fatal("stateful Responses input received values")
		}
	}
}

func TestPayloadLastClusterErrors(t *testing.T) {
	h := enabledLastClusterHandlers()
	for _, derive := range []func(*requestctx.RequestContext){
		func(c *requestctx.RequestContext) { h.setChatLastClusterKeys(c, []byte(`{`)) },
		func(c *requestctx.RequestContext) { h.setChatLastClusterKeys(c, []byte(`{"messages":[]}`)) },
		func(c *requestctx.RequestContext) { h.setChatLastClusterKeys(c, []byte(`{"messages":[{"role":1}]}`)) },
		func(c *requestctx.RequestContext) { h.setMessagesLastClusterKeys(c, nil, nil) },
		func(c *requestctx.RequestContext) {
			h.setMessagesLastClusterKeys(c, []json.RawMessage{json.RawMessage(`{`)}, nil)
		},
		func(c *requestctx.RequestContext) {
			h.setMessagesLastClusterKeys(c, []json.RawMessage{json.RawMessage(`{"role":"user"}`)}, json.RawMessage(`{`))
		},
		func(c *requestctx.RequestContext) { h.setResponsesLastClusterKeys(c, nil, nil) },
		func(c *requestctx.RequestContext) {
			h.setResponsesLastClusterKeys(c, &openairesponses.CreateRequest{}, []byte(`{`))
		},
		func(c *requestctx.RequestContext) {
			h.setResponsesLastClusterKeys(c, &openairesponses.CreateRequest{}, []byte(`{"input":[]}`))
		},
		func(c *requestctx.RequestContext) {
			h.setResponsesLastClusterKeys(c, &openairesponses.CreateRequest{}, []byte(`{"input":null}`))
		},
		func(c *requestctx.RequestContext) {
			h.setResponsesLastClusterKeys(c, &openairesponses.CreateRequest{}, []byte(`{"input":[{"role":"user","content":"hello"}]}`))
		},
		func(c *requestctx.RequestContext) {
			h.setResponsesLastClusterKeys(c, &openairesponses.CreateRequest{Input: openairesponses.Input{Items: []openairesponses.InputItem{{Role: "user"}}}}, []byte(`{"input":1}`))
		},
	} {
		reqCtx := lastClusterPayloadContext()
		derive(reqCtx)
		if reqCtx.LastClusterLookupKey != "" || reqCtx.LastClusterWriteKey != "" {
			t.Fatal("error or empty input set values")
		}
		if reqCtx.SessionID != "existing-session" || reqCtx.CacheAffinityKey != "existing-affinity" {
			t.Fatal("error changed session affinity")
		}
	}
}

func TestPayloadLastClusterRawFields(t *testing.T) {
	for _, tc := range []struct{ name, old, changed string }{
		{"chat", `"trace":"a1"`, `"trace":"edited"`},
		{"chat/single_tool", `city\":\"A`, `city\":\"edited`},
		{"responses/tools", `"encrypted_content":"opaque1"`, `"encrypted_content":"edited"`},
		{"messages/system_tools_thinking", `"signature":"sig1"`, `"signature":"edited"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, fixture := range lastClusterChainFixtures() {
				if fixture.name != tc.name {
					continue
				}
				original, edited := lastClusterPayloadContext(), lastClusterPayloadContext()
				body := strings.Replace(fixture.turns[2], tc.old, tc.changed, 1)
				if body == fixture.turns[2] {
					t.Fatal("fixture mutation changed nothing")
				}
				h := enabledLastClusterHandlers()
				deriveLastClusterFixture(t, h, original, fixture.endpoint, fixture.turns[2])
				deriveLastClusterFixture(t, h, edited, fixture.endpoint, body)
				if original.LastClusterLookupKey == edited.LastClusterLookupKey || original.LastClusterWriteKey == edited.LastClusterWriteKey {
					t.Fatal("changed raw prefix field retained a value")
				}
			}
		})
	}
}

func TestPayloadLastClusterTenantScope(t *testing.T) {
	for _, fixture := range lastClusterChainFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			h := enabledLastClusterHandlers()
			original := lastClusterPayloadContext()
			deriveLastClusterFixture(t, h, original, fixture.endpoint, fixture.turns[1])
			for _, field := range []string{"org", "project"} {
				other := lastClusterPayloadContext()
				if field == "org" {
					other.OrgID = "org-2"
				} else {
					other.ProjectID = "project-2"
				}
				deriveLastClusterFixture(t, h, other, fixture.endpoint, fixture.turns[1])
				if original.LastClusterLookupKey == other.LastClusterLookupKey || original.LastClusterWriteKey == other.LastClusterWriteKey {
					t.Fatalf("different %s shared a value", field)
				}
			}
		})
	}
}

func TestResponsesLastClusterOutputTypes(t *testing.T) {
	for _, tc := range []struct {
		item   string
		output bool
	}{
		{`{"role":"assistant","content":"hi"}`, true},
		{`{"type":"message","role":"assistant","content":"hi"}`, true},
		{`{"type":"reasoning","summary":[]}`, true},
		{`{"type":"function_call","call_id":"c1","name":"weather","arguments":"{}"}`, true},
		{`{"type":"custom_tool_call","call_id":"c1","name":"tool","input":"hello"}`, true},
		{`{"type":"web_search_call","id":"c1"}`, true},
		{`{"type":"code_interpreter_call","id":"c1"}`, true},
		{`{"type":"mcp_call","id":"c1"}`, true},
		{`{"type":"mcp_list_tools","id":"c1","tools":[]}`, true},
		{`{"type":"mcp_approval_request","id":"c1"}`, true},
		{`{"type":"function_call_output","call_id":"c1","output":"result"}`, false},
		{`{"type":"custom_tool_call_output","call_id":"c1","output":"result"}`, false},
		{`{"type":"mcp_approval_response","approval_request_id":"c1","approve":true}`, false},
		{`{"type":"message","role":"user","content":"hello"}`, false},
	} {
		t.Run(tc.item, func(t *testing.T) {
			h := enabledLastClusterHandlers()
			first := lastClusterPayloadContext()
			deriveLastClusterFixture(t, h, first, lastcluster.EndpointResponses, `{"input":[{"role":"user","content":"hello"}]}`)
			next := lastClusterPayloadContext()
			body := `{"input":[{"role":"user","content":"hello"},` + tc.item + `,{"role":"user","content":"next"}]}`
			deriveLastClusterFixture(t, h, next, lastcluster.EndpointResponses, body)
			if next.LastClusterWriteKey == "" {
				t.Fatal("missing write value")
			}
			if tc.output {
				if next.LastClusterLookupKey != first.LastClusterWriteKey {
					t.Fatal("model output did not cut before item")
				}
			} else if next.LastClusterLookupKey != "" {
				t.Fatal("client input created a cut")
			}
		})
	}
}

// lastClusterBenchmarkBody returns a request with 200 messages of about 2 KB
// each, alternating user and assistant and ending with a user turn.
func lastClusterBenchmarkBody(b *testing.B, field string) []byte {
	b.Helper()
	items := make([]map[string]string, 200)
	for i := range items {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		items[i] = map[string]string{"role": role, "content": fmt.Sprintf("%04d %s", i, strings.Repeat("x", 2043))}
	}
	body, err := json.Marshal(map[string]any{"model": "fn-alpha/company-name/model-name", field: items})
	if err != nil {
		b.Fatal(err)
	}
	return body
}

func BenchmarkChatLastClusterKeys(b *testing.B) {
	h, body := enabledLastClusterHandlers(), lastClusterBenchmarkBody(b, "messages")
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reqCtx := lastClusterPayloadContext()
		h.setChatLastClusterKeys(reqCtx, body)
		if reqCtx.LastClusterWriteKey == "" {
			b.Fatal("missing write value")
		}
	}
}

func BenchmarkResponsesLastClusterKeys(b *testing.B) {
	h, body := enabledLastClusterHandlers(), lastClusterBenchmarkBody(b, "input")
	var req openairesponses.CreateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reqCtx := lastClusterPayloadContext()
		h.setResponsesLastClusterKeys(reqCtx, &req, body)
		if reqCtx.LastClusterWriteKey == "" {
			b.Fatal("missing write value")
		}
	}
}
