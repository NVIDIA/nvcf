// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"
)

// bffSpec is the contract the UI's generated client is built from.
var bffSpec = filepath.Join("..", "..", "..", "spec", "bff-openapi.yaml")

// assertMatchesSpec fails the test unless body is valid against the schema
// spec/bff-openapi.yaml declares for the JSON response to method and path
// with status.
func assertMatchesSpec(t *testing.T, method, path, status string, body []byte) {
	t.Helper()
	if err := specViolation(method, path, status, body); err != nil {
		t.Errorf("%s %s: %v\n%s", method, path, err, body)
	}
}

// specViolation reports how body breaks the spec's schema for the response,
// or nil when it conforms. Formats such as `uri` are asserted, not just
// annotated.
func specViolation(method, path, status string, body []byte) error {
	raw, err := os.ReadFile(bffSpec)
	if err != nil {
		return err
	}
	asJSON, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return fmt.Errorf("parse %s: %w", bffSpec, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(asJSON))
	if err != nil {
		return err
	}

	response, err := responsePointer(doc, method, path, status)
	if err != nil {
		return err
	}

	const url = "file:///bff-openapi.json"
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	if err := compiler.AddResource(url, doc); err != nil {
		return err
	}
	schema, err := compiler.Compile(url + "#" + response + "/content/application~1json/schema")
	if err != nil {
		return fmt.Errorf("no JSON schema for the %s response: %w", status, err)
	}

	got, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("response is not JSON: %w", err)
	}
	if err := schema.Validate(got); err != nil {
		return fmt.Errorf("response doesn't match the spec: %w", err)
	}
	return nil
}

// responsePointer is the JSON Pointer of the response object for method, path
// and status, following a `$ref` to a shared response in components. The
// schema compiler resolves `$ref` only inside schemas, not response objects.
func responsePointer(doc any, method, path, status string) (string, error) {
	pointer := strings.Join([]string{
		"", "paths", escapePointer(path), strings.ToLower(method), "responses", status,
	}, "/")
	node := doc
	for _, token := range strings.Split(pointer, "/")[1:] {
		object, ok := node.(map[string]any)
		if !ok {
			return "", fmt.Errorf("%s not found in the spec", pointer)
		}
		if node, ok = object[strings.NewReplacer("~1", "/", "~0", "~").Replace(token)]; !ok {
			return "", fmt.Errorf("%s not found in the spec", pointer)
		}
	}
	if object, ok := node.(map[string]any); ok {
		if ref, ok := object["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
			return strings.TrimPrefix(ref, "#"), nil
		}
	}
	return pointer, nil
}

// escapePointer escapes one JSON Pointer reference token (RFC 6901).
func escapePointer(token string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(token)
}

// TestSpecCheckRejectsDrift proves the contract check has teeth: responses
// that break the spec in different ways all fail it.
func TestSpecCheckRejectsDrift(t *testing.T) {
	for name, body := range map[string]string{
		"undeclared field":  `{"gatewayUrl":"https://gw.example.com","extra":true}`,
		"not a URI":         `{"grafanaUrl":"not a url"}`,
		"protocol-relative": `{"grafanaUrl":"//grafana.example.com/d/x"}`,
		"wrong type":        `{"gatewayUrl":42}`,
		"not JSON":          `<!doctype html>`,
	} {
		t.Run(name, func(t *testing.T) {
			if specViolation("GET", "/api/v1/config", "200", []byte(body)) == nil {
				t.Errorf("body %s passed the spec check, want a failure", body)
			}
		})
	}

	t.Run("undeclared route", func(t *testing.T) {
		if specViolation("GET", "/api/v1/nope", "200", []byte(`{}`)) == nil {
			t.Error("a route missing from the spec passed the check, want a failure")
		}
	})
}
