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

package clusterdump

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// maskValue masks a sensitive string, preserving a hint of its length without
// revealing content. Mirrors the masking used elsewhere in the CLI (the cmd
// package cannot be imported by an internal package, so the logic lives here).
func maskValue(s string) string {
	switch n := len(s); {
	case n == 0:
		return ""
	case n <= 8:
		return strings.Repeat("*", n)
	case n <= 20:
		return s[:2] + strings.Repeat("*", n-4) + s[n-2:]
	default:
		return s[:4] + strings.Repeat("*", 12) + s[n-4:] + " (redacted)"
	}
}

// redactObject scrubs sensitive content from an unstructured object in place,
// according to the redaction mode. Keys are preserved so the object's shape
// stays useful for debugging; only values are masked. Redaction happens at
// capture time so raw secret material never reaches disk.
func redactObject(obj *unstructured.Unstructured, opts DumpOptions) {
	switch obj.GetKind() {
	case "Secret":
		if opts.redactSecrets() {
			maskStringMapField(obj.Object, "data")
			maskStringMapField(obj.Object, "stringData")
			maskStringMapField(obj.Object, "binaryData")
		}
	case "ConfigMap":
		if opts.redactAll() {
			maskStringMapField(obj.Object, "data")
			maskStringMapField(obj.Object, "binaryData")
		}
	}
}

// maskStringMapField masks every string value of a top-level map field
// (e.g. a Secret's "data") while keeping the keys.
func maskStringMapField(obj map[string]interface{}, field string) {
	raw, found, err := unstructured.NestedMap(obj, field)
	if !found || err != nil || len(raw) == 0 {
		return
	}
	for k, v := range raw {
		if s, ok := v.(string); ok {
			raw[k] = maskValue(s)
		}
	}
	_ = unstructured.SetNestedMap(obj, raw, field)
}
