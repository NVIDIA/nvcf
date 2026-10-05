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
// their plain text; objects and arrays come back as compact JSON.
func JSONField(raw, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("json path must be non-empty")
	}
	var node any
	if err := json.Unmarshal([]byte(raw), &node); err != nil {
		return "", fmt.Errorf("parse json: %w", err)
	}
	for _, part := range strings.Split(path, ".") {
		next, err := childNode(node, part)
		if err != nil {
			return "", fmt.Errorf("json path %q: %w", path, err)
		}
		node = next
	}
	return nodeText(node)
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
