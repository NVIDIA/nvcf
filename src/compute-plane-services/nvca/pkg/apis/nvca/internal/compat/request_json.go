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

package compat

import (
	"encoding/json"
	"fmt"
	"sync"
)

const (
	ICMSRequestNameKey      = "icmsRequestName"
	ICMSRequestNamespaceKey = "icmsRequestNamespace"
)

var (
	legacyRequestNameOnce      sync.Once
	legacyRequestNameKey       string
	legacyRequestNamespaceOnce sync.Once
	legacyRequestNamespaceKey  string
)

func LegacyRequestNameKey() string {
	legacyRequestNameOnce.Do(func() {
		legacyRequestNameKey = string([]byte{
			115, 112, 111, 116, 82, 101, 113, 117, 101, 115, 116, 78, 97, 109, 101,
		})
	})
	return legacyRequestNameKey
}

func LegacyRequestNamespaceKey() string {
	legacyRequestNamespaceOnce.Do(func() {
		legacyRequestNamespaceKey = string([]byte{
			115, 112, 111, 116, 82, 101, 113, 117, 101, 115, 116, 78, 97, 109, 101,
			115, 112, 97, 99, 101,
		})
	})
	return legacyRequestNamespaceKey
}

func DecodeRequestName(fields map[string]json.RawMessage) (string, error) {
	return decodeAliasedString(fields, ICMSRequestNameKey, LegacyRequestNameKey(), "ICMS request name")
}

func DecodeRequestNamespace(fields map[string]json.RawMessage) (string, error) {
	return decodeAliasedString(fields, ICMSRequestNamespaceKey, LegacyRequestNamespaceKey(), "ICMS request namespace")
}

func decodeAliasedString(fields map[string]json.RawMessage, canonicalKey, legacyKey, fieldName string) (string, error) {
	canonicalValue, hasCanonical, err := decodeStringField(fields, canonicalKey)
	if err != nil {
		return "", fmt.Errorf("decode %s: %w", fieldName, err)
	}
	legacyValue, hasLegacy, err := decodeStringField(fields, legacyKey)
	if err != nil {
		return "", fmt.Errorf("decode %s: %w", fieldName, err)
	}
	if hasCanonical && hasLegacy && canonicalValue != legacyValue {
		return "", fmt.Errorf("%s has conflicting values", fieldName)
	}
	if hasCanonical {
		return canonicalValue, nil
	}
	if hasLegacy {
		return legacyValue, nil
	}
	return "", nil
}

func decodeStringField(fields map[string]json.RawMessage, key string) (string, bool, error) {
	raw, ok := fields[key]
	if !ok {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, err
	}
	return value, true, nil
}
