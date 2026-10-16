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
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"math/big"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Add a case by adding a JSON file under testdata/fidelity. Files are discovered
// by both go test and Bazel. Copy the closest fixture and keep expectations
// independent of the production request/response models and rewrite helpers.
//
// Each fixture contains the endpoint path, client request, complete expected
// upstream_request, upstream_response, and complete expected client response.
// A response has either a json object or an sse array of {event, data} objects.
// event is optional; the data string "[DONE]" represents the chat terminator.
// Other data values are JSON objects. No fields are ignored in comparisons.
//
// Expected requests explicitly account for gateway-owned rewrites:
//   - All endpoints strip the function prefix from model.
//   - Chat and Responses force upstream stream=true, even for unary clients.
//   - Chat resolves service_tier and removes debug. For unary clients only, it
//     sets stream_options.include_usage=true, retaining other option members.
//
// Chat restores the client model in responses and assembles unary responses
// from SSE deltas. Responses relays SSE or extracts the terminal response
// object for unary clients. Embeddings relays the upstream JSON unchanged.
// Fixtures specify the expected result of those operations, including fields
// the gateway does not model. SSE comparison checks every event in order,
// including its name and terminator, without depending on JSON whitespace.
type fidelityFixture struct {
	Description      string          `json:"description"`
	Path             string          `json:"path"`
	Request          json.RawMessage `json:"request"`
	UpstreamRequest  json.RawMessage `json:"upstream_request"`
	UpstreamResponse fidelityBody    `json:"upstream_response"`
	Response         fidelityBody    `json:"response"`
}

type fidelityBody struct {
	JSON json.RawMessage `json:"json,omitempty"`
	SSE  []fidelityEvent `json:"sse,omitempty"`
}

type fidelityEvent struct {
	Event string          `json:"event,omitempty"`
	Data  json.RawMessage `json:"data"`
}

func TestGatewayRequestResponseFidelityCorpus(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("testdata/fidelity/*.json")
	require.NoError(t, err)
	require.NotEmpty(t, files, "the fidelity corpus must be present, including in Bazel runfiles")
	for _, file := range files {
		t.Run(strings.TrimSuffix(filepath.Base(file), ".json"), func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(file)
			require.NoError(t, err)
			var fixture fidelityFixture
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			require.NoError(t, decoder.Decode(&fixture))
			require.Equal(t, io.EOF, decoder.Decode(new(any)), "unexpected trailing fixture data")
			require.NotEmpty(t, fixture.Description)
			require.Contains(t, []string{"/v1/chat/completions", "/v1/responses", "/v1/embeddings"}, fixture.Path)
			t.Log(fixture.Description)

			contentType, upstreamBody := fixture.UpstreamResponse.encode(t)
			upstream := newPassthroughUpstream(t, contentType, upstreamBody)
			gateway := newPassthroughAPI(t, upstream.server.URL)
			response := servePassthroughRequest(t, gateway, fixture.Path, string(fixture.Request))

			require.Len(t, upstream.requests, 1, "client request must reach upstream: status=%d body=%s", response.Code, response.Body.String())
			t.Run("request", func(t *testing.T) {
				requireFidelityJSON(t, fixture.UpstreamRequest, <-upstream.requests)
			})
			t.Run("response", func(t *testing.T) {
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				wantType, _ := fixture.Response.encode(t)
				gotType, _, err := mime.ParseMediaType(response.Header().Get("Content-Type"))
				require.NoError(t, err)
				require.Equal(t, wantType, gotType)
				if fixture.Response.JSON != nil {
					requireFidelityJSON(t, fixture.Response.JSON, response.Body.Bytes())
					return
				}
				got := readFidelityEvents(t, response.Body.String())
				require.Len(t, got, len(fixture.Response.SSE), "SSE events must not be added or lost")
				for i, want := range fixture.Response.SSE {
					require.Equal(t, want.Event, got[i].Event, "event %d name", i)
					t.Run("event-"+strconv.Itoa(i), func(t *testing.T) {
						requireFidelityJSON(t, want.Data, got[i].Data)
					})
				}
			})
		})
	}
}

func (body fidelityBody) encode(t *testing.T) (string, string) {
	t.Helper()
	require.NotEqual(t, body.JSON != nil, body.SSE != nil, "response must have exactly one of json or sse")
	if body.JSON != nil {
		require.True(t, json.Valid(body.JSON))
		return "application/json", string(body.JSON)
	}
	require.NotEmpty(t, body.SSE)
	var wire strings.Builder
	for _, event := range body.SSE {
		if event.Event != "" {
			wire.WriteString("event: " + event.Event + "\n")
		}
		var payload bytes.Buffer
		require.NoError(t, json.Compact(&payload, event.Data))
		if payload.String() == `"[DONE]"` {
			wire.WriteString("data: [DONE]\n\n")
		} else {
			require.IsType(t, map[string]any{}, decodeFidelityJSON(t, event.Data), "SSE data must be an object or [DONE]")
			wire.WriteString("data: " + payload.String() + "\n\n")
		}
	}
	return "text/event-stream", wire.String()
}

func readFidelityEvents(t *testing.T, wire string) []fidelityEvent {
	t.Helper()
	var events []fidelityEvent
	var name string
	var data []string
	scanner := bufio.NewScanner(strings.NewReader(wire))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data != nil {
				payload := strings.Join(data, "\n")
				if payload == "[DONE]" {
					payload = `"[DONE]"`
				}
				events = append(events, fidelityEvent{Event: name, Data: json.RawMessage(payload)})
			}
			name, data = "", nil
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			name = value
		case "data":
			data = append(data, value)
		default:
			require.FailNow(t, "unexpected SSE field", "%q", line)
		}
	}
	require.NoError(t, scanner.Err())
	require.Empty(t, data, "last SSE event must end with a blank line")
	require.Empty(t, name, "last SSE event must end with a blank line")
	return events
}

func requireFidelityJSON(t *testing.T, want, got []byte) {
	t.Helper()
	require.Equal(t, decodeFidelityJSON(t, want), decodeFidelityJSON(t, got), "complete JSON payload differs")
}

func decodeFidelityJSON(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	require.NoError(t, decoder.Decode(&value), string(data))
	require.Equal(t, io.EOF, decoder.Decode(new(any)), "unexpected trailing JSON data")
	return normalizeFidelityNumbers(t, value)
}

// Compare numbers exactly without float64 rounding, while allowing equivalent
// JSON spellings such as 1 and 1.0. Strings, nulls, booleans and arrays retain
// their distinct types; missing object keys remain distinguishable from null.
func normalizeFidelityNumbers(t *testing.T, value any) any {
	t.Helper()
	switch value := value.(type) {
	case json.Number:
		rational, ok := new(big.Rat).SetString(string(value))
		require.True(t, ok, "invalid JSON number %q", value)
		return rational
	case map[string]any:
		for key, member := range value {
			value[key] = normalizeFidelityNumbers(t, member)
		}
		return value
	case []any:
		for i, member := range value {
			value[i] = normalizeFidelityNumbers(t, member)
		}
		return value
	default:
		return value
	}
}
