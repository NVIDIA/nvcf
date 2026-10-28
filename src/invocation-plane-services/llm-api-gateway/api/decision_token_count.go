// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// estimatedTokenCountForDecisionBody estimates the input tokens of a decision
// route request without knowing its schema. Every string leaf counts as text.
// Each image slot counts a flat imageTokens and its payload is never counted,
// so a large base64 image cannot hide or inflate the estimate. Image slots are
// objects with type "input_image", each element of an "images" array, each
// value of an "assets" object, and any string that is a base64 data URL.
func estimatedTokenCountForDecisionBody(raw []byte, imageTokens int) int {
	trimmed := bytes.TrimSpace(raw)
	if !json.Valid(trimmed) {
		return estimatedTokenCountForText(string(raw))
	}
	return decisionValueTokens(trimmed, imageTokens)
}

func decisionValueTokens(raw []byte, imageTokens int) int {
	switch firstJSONByte(raw) {
	case '"':
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return 0
		}
		if isBase64DataURL(text) {
			return imageTokens
		}
		return estimatedTokenCountForText(text)
	case '[':
		total := 0
		for _, element := range decodeJSONArray(raw) {
			total += decisionValueTokens(element, imageTokens)
		}
		return total
	case '{':
		members := decodeJSONObject(raw)
		for _, member := range members {
			if member.key == "type" && string(member.value) == `"input_image"` {
				return imageTokens
			}
		}
		total := 0
		for _, member := range members {
			switch {
			case member.key == "images" && firstJSONByte(member.value) == '[':
				total += len(decodeJSONArray(member.value)) * imageTokens
			case member.key == "assets" && firstJSONByte(member.value) == '{':
				total += len(decodeJSONObject(member.value)) * imageTokens
			default:
				total += decisionValueTokens(member.value, imageTokens)
			}
		}
		return total
	default:
		return 0
	}
}

func isBase64DataURL(text string) bool {
	return strings.HasPrefix(text, "data:") && strings.Contains(text, ";base64,")
}

func firstJSONByte(raw []byte) byte {
	if len(raw) == 0 {
		return 0
	}
	return raw[0]
}

type jsonMember struct {
	key   string
	value json.RawMessage
}

// decodeJSONObject returns the members of a JSON object in order, keeping
// duplicate keys so none of their content escapes the estimate.
func decodeJSONObject(raw []byte) []jsonMember {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil
	}
	var members []jsonMember
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return members
		}
		name, _ := key.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return members
		}
		members = append(members, jsonMember{key: name, value: value})
	}
	return members
}

func decodeJSONArray(raw []byte) []json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil
	}
	var elements []json.RawMessage
	for dec.More() {
		var element json.RawMessage
		if err := dec.Decode(&element); err != nil {
			if err == io.EOF {
				break
			}
			return elements
		}
		elements = append(elements, element)
	}
	return elements
}
