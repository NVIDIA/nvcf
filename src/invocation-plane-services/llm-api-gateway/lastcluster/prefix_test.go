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

package lastcluster

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The input builders below split wire bodies the way the API handlers do, so
// the fixtures read as real requests.

func outputFlags(t *testing.T, items []json.RawMessage, isOutput func(role, typ string) bool) []bool {
	t.Helper()
	out := make([]bool, len(items))
	for i, item := range items {
		var head struct{ Role, Type string }
		if err := json.Unmarshal(item, &head); err != nil {
			t.Fatalf("item %d: %v", i, err)
		}
		out[i] = isOutput(head.Role, head.Type)
	}
	return out
}

func roleAssistant(role, _ string) bool { return role == "assistant" }

func chatInput(t *testing.T, body string) PayloadInput {
	t.Helper()
	var req struct{ Messages []json.RawMessage }
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("chat body: %v", err)
	}
	return PayloadInput{
		Endpoint: EndpointChatCompletions,
		OrgID:    "org-1",
		Items:    req.Messages,
		Output:   outputFlags(t, req.Messages, roleAssistant),
	}
}

func messagesInput(t *testing.T, body string) PayloadInput {
	t.Helper()
	var req struct {
		System   json.RawMessage
		Messages []json.RawMessage
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("messages body: %v", err)
	}
	return PayloadInput{
		Endpoint: EndpointMessages,
		OrgID:    "org-1",
		Leading:  []json.RawMessage{req.System},
		Items:    req.Messages,
		Output:   outputFlags(t, req.Messages, roleAssistant),
	}
}

var responsesOutputTypes = map[string]bool{
	"reasoning":             true,
	"function_call":         true,
	"custom_tool_call":      true,
	"web_search_call":       true,
	"code_interpreter_call": true,
	"mcp_call":              true,
	"mcp_list_tools":        true,
	"mcp_approval_request":  true,
}

func responsesInput(t *testing.T, body string) PayloadInput {
	t.Helper()
	var req struct{ Input, Instructions, Prompt json.RawMessage }
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("responses body: %v", err)
	}
	var items []json.RawMessage
	var text string
	if err := json.Unmarshal(req.Input, &text); err == nil {
		item, _ := json.Marshal(map[string]string{"content": text, "role": "user"})
		items = []json.RawMessage{item}
	} else if err := json.Unmarshal(req.Input, &items); err != nil {
		t.Fatalf("responses input: %v", err)
	}
	return PayloadInput{
		Endpoint: EndpointResponses,
		OrgID:    "org-1",
		Leading:  []json.RawMessage{req.Instructions, req.Prompt},
		Items:    items,
		Output: outputFlags(t, items, func(role, typ string) bool {
			return responsesOutputTypes[typ] || ((typ == "" || typ == "message") && role == "assistant")
		}),
	}
}

func values(t *testing.T, in PayloadInput) (lookup, write string) {
	t.Helper()
	lookup, write, err := PayloadSessionValues(in)
	if err != nil {
		t.Fatalf("PayloadSessionValues() error = %v", err)
	}
	if !strings.HasPrefix(write, "pc:v1:") || len(write) != len("pc:v1:")+64 {
		t.Fatalf("write = %q, want pc:v1: and a hex SHA-256", write)
	}
	if lookup != "" && (!strings.HasPrefix(lookup, "pc:v1:") || len(lookup) != len(write)) {
		t.Fatalf("lookup = %q, want pc:v1: and a hex SHA-256", lookup)
	}
	return lookup, write
}

func TestPayloadSessionValuesChainAcrossTurns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(*testing.T, string) PayloadInput
		turns []string
	}{
		{
			name:  "chat multi-turn",
			build: chatInput,
			turns: []string{
				`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"u1"}]}`,
				`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"u1"},
					{"role":"assistant","content":"a1"},{"role":"user","content":"u2"}]}`,
				`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"u1"},
					{"role":"assistant","content":"a1"},{"role":"user","content":"u2"},
					{"role":"assistant","content":"a2"},{"role":"user","content":"u3"}]}`,
			},
		},
		{
			name:  "chat tool loop with single and parallel calls",
			build: chatInput,
			turns: []string{
				`{"messages":[{"role":"user","content":"weather?"}]}`,
				`{"messages":[{"role":"user","content":"weather?"},
					{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"geo","arguments":"{}"}}]},
					{"role":"tool","tool_call_id":"c1","content":"SF"}]}`,
				`{"messages":[{"role":"user","content":"weather?"},
					{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"geo","arguments":"{}"}}]},
					{"role":"tool","tool_call_id":"c1","content":"SF"},
					{"role":"assistant","content":null,"tool_calls":[
						{"id":"c2","type":"function","function":{"name":"temp","arguments":"{\"city\":\"SF\"}"}},
						{"id":"c3","type":"function","function":{"name":"wind","arguments":"{\"city\":\"SF\"}"}}]},
					{"role":"tool","tool_call_id":"c2","content":"18C"},
					{"role":"tool","tool_call_id":"c3","content":"5kt"}]}`,
			},
		},
		{
			name:  "responses tool loop from string input",
			build: responsesInput,
			turns: []string{
				`{"instructions":"be brief","input":"weather?"}`,
				`{"instructions":"be brief","input":[{"role":"user","content":"weather?"},
					{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"enc1"},
					{"type":"function_call","call_id":"c1","name":"geo","arguments":"{}"},
					{"type":"function_call_output","call_id":"c1","output":"SF"}]}`,
				`{"instructions":"be brief","input":[{"role":"user","content":"weather?"},
					{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"enc1"},
					{"type":"function_call","call_id":"c1","name":"geo","arguments":"{}"},
					{"type":"function_call_output","call_id":"c1","output":"SF"},
					{"type":"message","role":"assistant","content":[{"type":"output_text","text":"18C"}]},
					{"role":"user","content":"and tomorrow?"}]}`,
			},
		},
		{
			name:  "messages with system, thinking, tool_use, and parallel tool_result",
			build: messagesInput,
			turns: []string{
				`{"system":[{"type":"text","text":"s"}],"messages":[{"role":"user","content":"weather?"}]}`,
				`{"system":[{"type":"text","text":"s"}],"messages":[{"role":"user","content":"weather?"},
					{"role":"assistant","content":[
						{"type":"thinking","thinking":"need tools","signature":"sig1"},
						{"type":"tool_use","id":"t1","name":"temp","input":{"city":"SF"}},
						{"type":"tool_use","id":"t2","name":"wind","input":{"city":"SF"}}]},
					{"role":"user","content":[
						{"type":"tool_result","tool_use_id":"t1","content":"18C"},
						{"type":"tool_result","tool_use_id":"t2","content":"5kt"}]}]}`,
				`{"system":[{"type":"text","text":"s"}],"messages":[{"role":"user","content":"weather?"},
					{"role":"assistant","content":[
						{"type":"thinking","thinking":"need tools","signature":"sig1"},
						{"type":"tool_use","id":"t1","name":"temp","input":{"city":"SF"}},
						{"type":"tool_use","id":"t2","name":"wind","input":{"city":"SF"}}]},
					{"role":"user","content":[
						{"type":"tool_result","tool_use_id":"t1","content":"18C"},
						{"type":"tool_result","tool_use_id":"t2","content":"5kt"}]},
					{"role":"assistant","content":[{"type":"text","text":"18C, light wind"}]},
					{"role":"user","content":"thanks"}]}`,
			},
		},
		{
			name:  "messages without system",
			build: messagesInput,
			turns: []string{
				`{"messages":[{"role":"user","content":"u1"}]}`,
				`{"messages":[{"role":"user","content":"u1"},{"role":"assistant","content":"a1"},{"role":"user","content":"u2"}]}`,
				`{"messages":[{"role":"user","content":"u1"},{"role":"assistant","content":"a1"},{"role":"user","content":"u2"},
					{"role":"assistant","content":"a2"},{"role":"user","content":"u3"}]}`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := map[string]bool{}
			var prevWrite string
			for i, body := range tc.turns {
				lookup, write := values(t, tc.build(t, body))
				if i == 0 && lookup != "" {
					t.Fatalf("turn 1 lookup = %q, want empty", lookup)
				}
				if i > 0 && lookup != prevWrite {
					t.Fatalf("turn %d lookup = %q, want turn %d write %q", i+1, lookup, i, prevWrite)
				}
				if seen[write] {
					t.Fatalf("turn %d write repeats an earlier value", i+1)
				}
				seen[write] = true
				prevWrite = write
			}
		})
	}
}

func TestPayloadSessionValuesNormalization(t *testing.T) {
	base := `{"messages":[{"role":"user","content":"u1"},{"role":"assistant","content":"a1","name":"bot"},{"role":"user","content":"A <b>"}]}`
	baseLookup, baseWrite := values(t, chatInput(t, base))

	variant := `{
  "messages": [
    {"content": "u1", "role": "user"},
    {"name":"bot",   "content":"a1","role":"assistant"},
    { "content" : "\u0041 \u003cb\u003e" , "role" : "user" }
  ]
}`
	if lookup, write := values(t, chatInput(t, variant)); lookup != baseLookup || write != baseWrite {
		t.Fatal("whitespace, key order, or string escapes changed values")
	}

	intLookup, intWrite := values(t, chatInput(t, `{"messages":[{"role":"user","content":"u","n":1}]}`))
	floatLookup, floatWrite := values(t, chatInput(t, `{"messages":[{"role":"user","content":"u","n":1.0}]}`))
	if intLookup != "" || floatLookup != "" {
		t.Fatal("single user message must have no lookup value")
	}
	if intWrite == floatWrite {
		t.Fatal("number literals 1 and 1.0 must yield different values")
	}
}

func TestPayloadSessionValuesPrefixSensitivity(t *testing.T) {
	turn2 := func(system, u1, a1, u2 string) string {
		return fmt.Sprintf(`{"system":%q,"messages":[{"role":"user","content":%q},{"role":"assistant","content":%q},{"role":"user","content":%q}]}`,
			system, u1, a1, u2)
	}
	baseLookup, baseWrite := values(t, messagesInput(t, turn2("s", "u1", "a1", "u2")))

	for name, body := range map[string]string{
		"edited earlier user content": turn2("s", "u1 edited", "a1", "u2"),
		"changed system":              turn2("s2", "u1", "a1", "u2"),
	} {
		lookup, write := values(t, messagesInput(t, body))
		if lookup == baseLookup || write == baseWrite {
			t.Fatalf("%s: values must change", name)
		}
	}

	// The cut is before a1, so editing a1 or u2 only changes write.
	for name, body := range map[string]string{
		"edited model output after cut": turn2("s", "u1", "a1 edited", "u2"),
		"edited last user message":      turn2("s", "u1", "a1", "u2 edited"),
	} {
		lookup, write := values(t, messagesInput(t, body))
		if lookup != baseLookup || write == baseWrite {
			t.Fatalf("%s: want same lookup and different write", name)
		}
	}

	// An assistant edit inside the prefix changes the next turn's lookup.
	turn3 := func(a1 string) string {
		return fmt.Sprintf(`{"messages":[{"role":"user","content":"u1"},{"role":"assistant","content":%q},`+
			`{"role":"user","content":"u2"},{"role":"assistant","content":"a2"},{"role":"user","content":"u3"}]}`, a1)
	}
	original, _ := values(t, chatInput(t, turn3("a1")))
	edited, _ := values(t, chatInput(t, turn3("a1 edited")))
	if original == edited {
		t.Fatal("edited assistant content inside the prefix must change lookup")
	}

	instructions := func(text string) string {
		return fmt.Sprintf(`{"instructions":%q,"input":[{"role":"user","content":"u1"},`+
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a1"}]},`+
			`{"role":"user","content":"u2"}]}`, text)
	}
	original, _ = values(t, responsesInput(t, instructions("be brief")))
	edited, _ = values(t, responsesInput(t, instructions("be verbose")))
	if original == edited {
		t.Fatal("changed instructions must change lookup")
	}
}

func TestPayloadSessionValuesSharedSystemPromptDoesNotMerge(t *testing.T) {
	conversation := func(u1 string) []string {
		return []string{
			fmt.Sprintf(`{"messages":[{"role":"system","content":"shared"},{"role":"user","content":%q}]}`, u1),
			fmt.Sprintf(`{"messages":[{"role":"system","content":"shared"},{"role":"user","content":%q},`+
				`{"role":"assistant","content":"a1"},{"role":"user","content":"u2"}]}`, u1),
		}
	}
	collect := func(u1 string) map[string]bool {
		out := map[string]bool{}
		for _, body := range conversation(u1) {
			lookup, write := values(t, chatInput(t, body))
			out[write] = true
			if lookup != "" {
				out[lookup] = true
			}
		}
		return out
	}
	a, b := collect("first question"), collect("other question")
	for value := range a {
		if b[value] {
			t.Fatalf("conversations with different first user messages share %q", value)
		}
	}
}

func TestPayloadSessionValuesScope(t *testing.T) {
	items := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"u1"}`),
		json.RawMessage(`{"role":"assistant","content":"a1"}`),
		json.RawMessage(`{"role":"user","content":"u2"}`),
	}
	output := []bool{false, true, false}
	base := PayloadInput{Endpoint: EndpointChatCompletions, OrgID: "org-1", ProjectID: "proj-1", Items: items, Output: output}

	variants := map[string]PayloadInput{
		"base": base,
		"messages endpoint": {Endpoint: EndpointMessages, OrgID: "org-1", ProjectID: "proj-1",
			Leading: []json.RawMessage{nil}, Items: items, Output: output},
		"other org":     {Endpoint: EndpointChatCompletions, OrgID: "org-2", ProjectID: "proj-1", Items: items, Output: output},
		"other project": {Endpoint: EndpointChatCompletions, OrgID: "org-1", ProjectID: "proj-2", Items: items, Output: output},
		"org and project split differently": {Endpoint: EndpointChatCompletions, OrgID: "org-1p", ProjectID: "roj-1",
			Items: items, Output: output},
		"messages with empty system": {Endpoint: EndpointMessages, OrgID: "org-1", ProjectID: "proj-1",
			Leading: []json.RawMessage{json.RawMessage(`""`)}, Items: items, Output: output},
	}
	seen := map[string]string{}
	for name, in := range variants {
		lookup, write := values(t, in)
		for _, value := range []string{lookup, write} {
			if other, ok := seen[value]; ok {
				t.Fatalf("%s and %s share value %q", name, other, value)
			}
			seen[value] = name
		}
	}
}

func TestPayloadSessionValuesLookupBoundary(t *testing.T) {
	msg := func(role string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"role":%q,"content":"x"}`, role))
	}
	write := func(n int, items []json.RawMessage, output []bool) string {
		_, w := values(t, PayloadInput{Endpoint: EndpointChatCompletions, Items: items[:n], Output: output[:n]})
		return w
	}
	for _, tc := range []struct {
		name   string
		output []bool
		// prefix is the number of items the lookup covers, or -1 for none.
		prefix int
	}{
		{name: "no output", output: []bool{false, false}, prefix: -1},
		{name: "output run at index 0", output: []bool{true, false}, prefix: -1},
		{name: "only output", output: []bool{true, true}, prefix: -1},
		{name: "ends in output", output: []bool{false, true}, prefix: 1},
		{name: "ends in output run", output: []bool{false, true, false, true, true}, prefix: 3},
		{name: "contiguous output run", output: []bool{false, true, true, false}, prefix: 1},
		{name: "last run wins", output: []bool{false, true, false, true, false}, prefix: 3},
		{name: "empty", output: []bool{}, prefix: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := make([]json.RawMessage, len(tc.output))
			for i, out := range tc.output {
				items[i] = msg(map[bool]string{true: "assistant", false: "user"}[out])
			}
			lookup, _ := values(t, PayloadInput{Endpoint: EndpointChatCompletions, Items: items, Output: tc.output})
			want := ""
			if tc.prefix >= 0 {
				want = write(tc.prefix, items, tc.output)
			}
			if lookup != want {
				t.Fatalf("lookup = %q, want %q", lookup, want)
			}
		})
	}
}

func TestPayloadSessionValuesErrors(t *testing.T) {
	valid := json.RawMessage(`{"role":"user","content":"u"}`)
	for name, in := range map[string]PayloadInput{
		"unknown endpoint":     {Endpoint: "completions", Items: []json.RawMessage{valid}, Output: []bool{false}},
		"output length":        {Endpoint: EndpointChatCompletions, Items: []json.RawMessage{valid}, Output: []bool{false, true}},
		"leading slot count":   {Endpoint: EndpointMessages, Items: []json.RawMessage{valid}, Output: []bool{false}},
		"invalid item JSON":    {Endpoint: EndpointChatCompletions, Items: []json.RawMessage{json.RawMessage(`{"role":`)}, Output: []bool{false}},
		"empty item":           {Endpoint: EndpointChatCompletions, Items: []json.RawMessage{json.RawMessage(``)}, Output: []bool{false}},
		"trailing item data":   {Endpoint: EndpointChatCompletions, Items: []json.RawMessage{json.RawMessage(`{} {}`)}, Output: []bool{false}},
		"invalid leading JSON": {Endpoint: EndpointMessages, Leading: []json.RawMessage{json.RawMessage(`[`)}, Items: []json.RawMessage{valid}, Output: []bool{false}},
	} {
		t.Run(name, func(t *testing.T) {
			lookup, write, err := PayloadSessionValues(in)
			if err == nil || lookup != "" || write != "" {
				t.Fatalf("PayloadSessionValues() = %q, %q, %v; want error and no values", lookup, write, err)
			}
		})
	}
}

func BenchmarkPayloadSessionValues(b *testing.B) {
	const messages = 200
	text := strings.Repeat("lorem ipsum dolor sit amet, ", 70) // about 2 KB
	items := make([]json.RawMessage, messages)
	output := make([]bool, messages)
	for i := range items {
		role := "user"
		if i%2 == 1 && i < messages-1 {
			role = "assistant"
			output[i] = true
		}
		raw, err := json.Marshal(map[string]any{"role": role, "content": fmt.Sprintf("%d %s", i, text)})
		if err != nil {
			b.Fatal(err)
		}
		items[i] = raw
	}
	in := PayloadInput{
		Endpoint:  EndpointMessages,
		OrgID:     "org-1",
		ProjectID: "proj-1",
		Leading:   []json.RawMessage{json.RawMessage(`"system prompt"`)},
		Items:     items,
		Output:    output,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := PayloadSessionValues(in); err != nil {
			b.Fatal(err)
		}
	}
}
