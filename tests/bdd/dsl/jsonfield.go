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

package dsl

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// JSONField reads the dotted path from a JSON document and returns the value
// as text. Object keys are matched by name and array elements by decimal
// index, so "events.0.event_name" reads the first event. Scalars come back as
// their plain text; objects and arrays come back as compact JSON. A path can
// continue into a string that holds a JSON object or array, such as a request
// an echo sample returns as its reply text.
func JSONField(raw, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("json path must be non-empty")
	}
	node, err := jsonNode(raw, strings.Split(path, "."))
	if err != nil {
		return "", fmt.Errorf("json path %q: %w", path, err)
	}
	return nodeText(node)
}

// JSONFieldAbsent reports an error unless the last key of the dotted path is
// missing from an object that exists, so a misspelled parent cannot pass.
func JSONFieldAbsent(raw, path string) error {
	parts := strings.Split(path, ".")
	if strings.TrimSpace(path) == "" || len(parts) < 2 {
		return fmt.Errorf("json path %q must name a key inside an object", path)
	}
	parent, err := jsonNode(raw, parts[:len(parts)-1])
	if err != nil {
		return fmt.Errorf("json path %q: %w", path, err)
	}
	object, ok := decodeEmbedded(parent).(map[string]any)
	if !ok {
		return fmt.Errorf("json path %q: parent is not an object", path)
	}
	if value, found := object[parts[len(parts)-1]]; found {
		text, _ := nodeText(value)
		return fmt.Errorf("json path %q is present with value %q, want absent", path, text)
	}
	return nil
}

func jsonNode(raw string, parts []string) (any, error) {
	var node any
	if err := json.Unmarshal([]byte(raw), &node); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	for _, part := range parts {
		next, err := childNode(decodeEmbedded(node), part)
		if err != nil {
			return nil, err
		}
		node = next
	}
	return node, nil
}

// decodeEmbedded returns the object or array a string holds as JSON text, and
// any other value unchanged.
func decodeEmbedded(node any) any {
	text, ok := node.(string)
	if !ok {
		return node
	}
	var embedded any
	if err := json.Unmarshal([]byte(text), &embedded); err != nil {
		return node
	}
	switch embedded.(type) {
	case map[string]any, []any:
		return embedded
	default:
		return node
	}
}

// JSONFieldAtLeast reports whether the numeric value at the dotted path is
// greater than or equal to min.
func JSONFieldAtLeast(raw, path string, min float64) error {
	text, err := JSONField(raw, path)
	if err != nil {
		return err
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("json path %q is not a number: %q", path, text)
	}
	if value < min {
		return fmt.Errorf("json path %q = %v, want at least %v", path, value, min)
	}
	return nil
}

func childNode(node any, part string) (any, error) {
	switch typed := node.(type) {
	case map[string]any:
		child, ok := typed[part]
		if !ok {
			return nil, fmt.Errorf("key %q not found", part)
		}
		return child, nil
	case []any:
		index, err := strconv.Atoi(part)
		if err != nil || index < 0 || index >= len(typed) {
			return nil, fmt.Errorf("index %q out of range for array of %d", part, len(typed))
		}
		return typed[index], nil
	default:
		return nil, fmt.Errorf("cannot descend into %q: value is not an object or array", part)
	}
}

func nodeText(node any) (string, error) {
	switch typed := node.(type) {
	case string:
		return typed, nil
	case nil:
		return "null", nil
	case map[string]any, []any:
		data, err := json.Marshal(typed)
		if err != nil {
			return "", fmt.Errorf("encode json: %w", err)
		}
		return string(data), nil
	default:
		return fmt.Sprint(typed), nil
	}
}
