/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"

	config "ai-api-gateway-service/gateway_config"
)

type shadowSamplingResult struct {
	bucket    int
	available bool
	set       bool
}

type shadowRequestSampler struct {
	request               *http.Request
	rawBody               []byte
	endpoint              shadowSamplingEndpoint
	promptCacheKeyHeaders []string
	randomBucket          func() int
	bodyFields            map[string]json.RawMessage
	bodyParsed            bool
	results               map[config.ShadowSamplingMethod]shadowSamplingResult
	decidedMethods        []config.ShadowSamplingMethod
}

func newShadowRequestSampler(
	request *http.Request,
	rawBody []byte,
	endpoint shadowSamplingEndpoint,
	promptCacheKeyHeaders []string,
	randomBucket func() int,
) *shadowRequestSampler {
	return &shadowRequestSampler{
		request:               request,
		rawBody:               rawBody,
		endpoint:              endpoint,
		promptCacheKeyHeaders: promptCacheKeyHeaders,
		randomBucket:          randomBucket,
		results:               make(map[config.ShadowSamplingMethod]shadowSamplingResult, 4),
	}
}

func admittedShadows(req *http.Request, shadows []shadowConfig, randomBucket func() int) []shadowConfig {
	return admittedShadowsWithSampler(shadows, newShadowRequestSampler(
		req,
		nil,
		shadowSamplingEndpointNone,
		nil,
		randomBucket,
	))
}

func admittedShadowsWithSampler(shadows []shadowConfig, sampler *shadowRequestSampler) []shadowConfig {
	var admitted []shadowConfig
	for _, shadow := range shadows {
		if shadow.percentage >= 100 {
			admitted = append(admitted, shadow)
			continue
		}
		for _, method := range shadow.samplingMethods {
			bucket, ok := sampler.bucket(method)
			if !ok {
				continue
			}
			isAdmitted := bucket < shadow.percentage
			sampler.recordDecision(method, isAdmitted)
			if isAdmitted {
				admitted = append(admitted, shadow)
			}
			break
		}
	}
	return admitted
}

// recordDecision counts the method that made a shadow's final admission
// decision and remembers it, once per method, for the request span.
func (s *shadowRequestSampler) recordDecision(method config.ShadowSamplingMethod, admitted bool) {
	ctx := context.Background()
	if s.request != nil {
		ctx = s.request.Context()
	}
	recordShadowSamplingDecision(ctx, s.endpoint, method, admitted)
	if !slices.Contains(s.decidedMethods, method) {
		s.decidedMethods = append(s.decidedMethods, method)
	}
}

func (s *shadowRequestSampler) bucket(method config.ShadowSamplingMethod) (int, bool) {
	if result, ok := s.results[method]; ok && result.set {
		return result.bucket, result.available
	}

	var material []byte
	var available bool
	switch method {
	case config.ShadowSamplingMethodRandom:
		result := shadowSamplingResult{bucket: s.randomBucket(), available: true, set: true}
		s.results[method] = result
		return result.bucket, true
	case config.ShadowSamplingMethodPerBearerKey:
		material, available = bearerCredential(s.request)
	case config.ShadowSamplingMethodPromptCacheKey:
		material, available = s.promptCacheKey()
	case config.ShadowSamplingMethodFirstMessageHash:
		material, available = s.firstMessageMaterial()
	}

	result := shadowSamplingResult{available: available, set: true}
	if available {
		result.bucket = shadowBucketForMaterial(material)
	}
	s.results[method] = result
	return result.bucket, result.available
}

func (s *shadowRequestSampler) promptCacheKey() ([]byte, bool) {
	// Messages has no prompt_cache_key body field, so only headers apply.
	if s.endpoint == shadowSamplingEndpointMessages {
		return s.promptCacheKeyFromHeaders()
	}
	if raw, ok := s.parsedBodyFields()["prompt_cache_key"]; ok {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			value = strings.TrimSpace(value)
			if value != "" {
				return []byte(value), true
			}
		}
	}

	return s.promptCacheKeyFromHeaders()
}

func (s *shadowRequestSampler) promptCacheKeyFromHeaders() ([]byte, bool) {
	if s.request == nil {
		return nil, false
	}
	for _, name := range s.promptCacheKeyHeaders {
		values := s.request.Header.Values(name)
		if len(values) != 1 {
			continue
		}
		value := strings.TrimSpace(values[0])
		if value != "" {
			return []byte(value), true
		}
	}
	return nil, false
}

func (s *shadowRequestSampler) firstMessageMaterial() ([]byte, bool) {
	fields := s.parsedBodyFields()
	if fields == nil {
		return nil, false
	}
	switch s.endpoint {
	case shadowSamplingEndpointChatCompletions:
		return canonicalChatFirstMessage(fields)
	case shadowSamplingEndpointResponses:
		// A request that continues earlier turns usually carries only its newest
		// input. The gateway cannot reliably identify the original first message
		// from it, and hashing the newest input would key the decision on the
		// follow-up text, not the conversation. Skip the method conservatively so
		// sampling moves to the next one.
		if continuesEarlierTurns(fields) {
			return nil, false
		}
		return canonicalResponsesFirstMessage(fields)
	case shadowSamplingEndpointMessages:
		return canonicalMessagesFirstMessage(fields)
	default:
		return nil, false
	}
}

// continuesEarlierTurns reports whether a Responses body points at turns the
// backend already stores, through previous_response_id or conversation. The
// gateway cannot reliably tell which message started such a conversation.
func continuesEarlierTurns(fields map[string]json.RawMessage) bool {
	return hasJSONValue(fields, "previous_response_id") || hasJSONValue(fields, "conversation")
}

// hasJSONValue reports whether a field holds a value. A missing field, null, an
// empty string, and an empty object count as no value. Anything else counts,
// including shapes the API does not define, so sampling errs toward skipping.
func hasJSONValue(fields map[string]json.RawMessage, name string) bool {
	raw, ok := fields[name]
	if !ok {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	switch value := value.(type) {
	case nil:
		return false
	case string:
		return value != ""
	case map[string]any:
		return len(value) > 0
	default:
		return true
	}
}

func (s *shadowRequestSampler) parsedBodyFields() map[string]json.RawMessage {
	if s.bodyParsed {
		return s.bodyFields
	}
	s.bodyParsed = true
	if len(s.rawBody) == 0 || json.Unmarshal(s.rawBody, &s.bodyFields) != nil {
		s.bodyFields = nil
	}
	return s.bodyFields
}

type canonicalShadowMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type canonicalFirstMessageEnvelope struct {
	Version      int                      `json:"version"`
	Endpoint     shadowSamplingEndpoint   `json:"endpoint"`
	Instructions any                      `json:"instructions,omitempty"`
	Messages     []canonicalShadowMessage `json:"messages"`
}

func canonicalChatFirstMessage(fields map[string]json.RawMessage) ([]byte, bool) {
	var messages []json.RawMessage
	if json.Unmarshal(fields["messages"], &messages) != nil {
		return nil, false
	}

	selected := make([]canonicalShadowMessage, 0, 3)
	for _, rawMessage := range messages {
		var message map[string]json.RawMessage
		if json.Unmarshal(rawMessage, &message) != nil {
			continue
		}
		var role string
		if json.Unmarshal(message["role"], &role) != nil {
			continue
		}
		switch role {
		case "system", "developer":
			content, ok := canonicalJSONValue(message["content"])
			if !ok {
				content = nil
			}
			selected = append(selected, canonicalShadowMessage{Role: role, Content: content})
		case "user":
			content, ok := nonemptyCanonicalJSONValue(message["content"])
			if !ok {
				return nil, false
			}
			selected = append(selected, canonicalShadowMessage{Role: role, Content: content})
			return marshalCanonicalFirstMessage(shadowSamplingEndpointChatCompletions, nil, selected)
		}
	}
	return nil, false
}

func canonicalResponsesFirstMessage(fields map[string]json.RawMessage) ([]byte, bool) {
	var instructions any
	if value, ok := nonemptyCanonicalJSONValue(fields["instructions"]); ok {
		instructions = value
	}

	input := fields["input"]
	var text string
	if json.Unmarshal(input, &text) == nil {
		if text == "" {
			return nil, false
		}
		return marshalCanonicalFirstMessage(
			shadowSamplingEndpointResponses,
			instructions,
			[]canonicalShadowMessage{{Role: "user", Content: text}},
		)
	}

	var items []json.RawMessage
	if json.Unmarshal(input, &items) != nil {
		return nil, false
	}
	for _, rawItem := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(rawItem, &item) != nil {
			continue
		}
		var role string
		if json.Unmarshal(item["role"], &role) != nil || role != "user" {
			continue
		}
		content, ok := nonemptyCanonicalJSONValue(item["content"])
		if !ok {
			return nil, false
		}
		return marshalCanonicalFirstMessage(
			shadowSamplingEndpointResponses,
			instructions,
			[]canonicalShadowMessage{{Role: role, Content: content}},
		)
	}
	return nil, false
}

// canonicalMessagesFirstMessage hashes the top-level system prompt and the
// first user message of an Anthropic Messages request. cache_control markers
// are dropped because clients move them between turns without changing the
// conversation.
func canonicalMessagesFirstMessage(fields map[string]json.RawMessage) ([]byte, bool) {
	var messages []json.RawMessage
	if json.Unmarshal(fields["messages"], &messages) != nil {
		return nil, false
	}

	selected := make([]canonicalShadowMessage, 0, 2)
	if system, ok := nonemptyCanonicalJSONValue(fields["system"]); ok {
		selected = append(selected, canonicalShadowMessage{Role: "system", Content: withoutCacheControl(system)})
	}
	for _, rawMessage := range messages {
		var message map[string]json.RawMessage
		if json.Unmarshal(rawMessage, &message) != nil {
			continue
		}
		var role string
		if json.Unmarshal(message["role"], &role) != nil || role != "user" {
			continue
		}
		content, ok := nonemptyCanonicalJSONValue(message["content"])
		if !ok {
			return nil, false
		}
		selected = append(selected, canonicalShadowMessage{Role: role, Content: withoutCacheControl(content)})
		return marshalCanonicalFirstMessage(shadowSamplingEndpointMessages, nil, selected)
	}
	return nil, false
}

// withoutCacheControl returns value with every cache_control object key removed.
func withoutCacheControl(value any) any {
	switch value := value.(type) {
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			result[i] = withoutCacheControl(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			if key == "cache_control" {
				continue
			}
			result[key] = withoutCacheControl(item)
		}
		return result
	default:
		return value
	}
}

func marshalCanonicalFirstMessage(
	endpoint shadowSamplingEndpoint,
	instructions any,
	messages []canonicalShadowMessage,
) ([]byte, bool) {
	material, err := json.Marshal(canonicalFirstMessageEnvelope{
		Version:      1,
		Endpoint:     endpoint,
		Instructions: instructions,
		Messages:     messages,
	})
	return material, err == nil
}

func nonemptyCanonicalJSONValue(raw json.RawMessage) (any, bool) {
	value, ok := canonicalJSONValue(raw)
	if !ok {
		return nil, false
	}
	switch value := value.(type) {
	case nil:
		return nil, false
	case string:
		return value, value != ""
	case []any:
		return value, len(value) > 0
	case map[string]any:
		return value, len(value) > 0
	default:
		return value, true
	}
}

func canonicalJSONValue(raw json.RawMessage) (any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, false
	}
	return value, true
}

func bearerCredential(req *http.Request) ([]byte, bool) {
	if req == nil {
		return nil, false
	}
	values := req.Header.Values("Authorization")
	if len(values) != 1 {
		return nil, false
	}

	const scheme = "Bearer"
	value := values[0]
	if len(value) <= len(scheme) || !strings.EqualFold(value[:len(scheme)], scheme) {
		return nil, false
	}

	remainder := value[len(scheme):]
	if remainder == "" || strings.TrimLeft(remainder, " \t") == remainder {
		return nil, false
	}

	credential := strings.TrimLeft(remainder, " \t")
	if credential == "" {
		return nil, false
	}
	return []byte(credential), true
}

func shadowBucketForBearerCredential(credential []byte) int {
	return shadowBucketForMaterial(credential)
}

func shadowBucketForMaterial(material []byte) int {
	digest := sha256.Sum256(material)
	return int(binary.BigEndian.Uint64(digest[:8]) % 100)
}
