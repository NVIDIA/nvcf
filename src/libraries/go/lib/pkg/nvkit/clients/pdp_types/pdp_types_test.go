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

package pdp_types

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"
)

func TestRuleRequestString_WithAllFields(t *testing.T) {
	strVal, err := structpb.NewValue("some-value")
	if err != nil {
		t.Fatalf("failed to create structpb value: %v", err)
	}

	rr := &RuleRequest{
		Namespace: "test-namespace",
		RuleName:  "test.policy",
		Input: map[string]*structpb.Value{
			"key1": strVal,
		},
	}

	result := rr.String()
	if result == "" {
		t.Fatal("expected non-empty string, got empty")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("String() returned invalid JSON: %v", err)
	}

	if parsed["namespace"] != "test-namespace" {
		t.Errorf("expected namespace %q, got %v", "test-namespace", parsed["namespace"])
	}
	if parsed["rule_name"] != "test.policy" {
		t.Errorf("expected rule_name %q, got %v", "test.policy", parsed["rule_name"])
	}
}

func TestRuleRequestString_Empty(t *testing.T) {
	rr := &RuleRequest{}

	result := rr.String()
	if result == "" {
		t.Fatal("expected non-empty JSON string for empty struct, got empty")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("String() returned invalid JSON: %v", err)
	}
}

func TestRuleRequestString_NilInput(t *testing.T) {
	rr := &RuleRequest{
		Namespace: "ns",
		RuleName:  "pkg.rule",
		Input:     nil,
	}

	result := rr.String()
	if result == "" {
		t.Fatal("expected non-empty string")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("String() returned invalid JSON: %v", err)
	}

	if parsed["namespace"] != "ns" {
		t.Errorf("expected namespace %q, got %v", "ns", parsed["namespace"])
	}
}

func TestRuleResponseString_WithAllFields(t *testing.T) {
	boolVal, err := structpb.NewValue(true)
	if err != nil {
		t.Fatalf("failed to create structpb value: %v", err)
	}

	rr := &RuleResponse{
		Namespace: "response-namespace",
		RuleName:  "response.policy",
		Result:    boolVal,
	}

	result := rr.String()
	if result == "" {
		t.Fatal("expected non-empty string, got empty")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("String() returned invalid JSON: %v", err)
	}

	if parsed["namespace"] != "response-namespace" {
		t.Errorf("expected namespace %q, got %v", "response-namespace", parsed["namespace"])
	}
	if parsed["rule_name"] != "response.policy" {
		t.Errorf("expected rule_name %q, got %v", "response.policy", parsed["rule_name"])
	}
	if parsed["result"] != true {
		t.Errorf("expected result true, got %v", parsed["result"])
	}
}

func TestRuleResponseString_Empty(t *testing.T) {
	rr := &RuleResponse{}

	result := rr.String()
	if result == "" {
		t.Fatal("expected non-empty JSON string for empty struct, got empty")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("String() returned invalid JSON: %v", err)
	}
}

func TestRuleResponseString_NilResult(t *testing.T) {
	rr := &RuleResponse{
		Namespace: "ns",
		RuleName:  "pkg.rule",
		Result:    nil,
	}

	result := rr.String()
	if result == "" {
		t.Fatal("expected non-empty string")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("String() returned invalid JSON: %v", err)
	}

	if parsed["namespace"] != "ns" {
		t.Errorf("expected namespace %q, got %v", "ns", parsed["namespace"])
	}
}

func TestRuleResponseString_WithStructResult(t *testing.T) {
	structVal, err := structpb.NewValue(map[string]interface{}{
		"allow":  true,
		"reason": "authorized",
	})
	if err != nil {
		t.Fatalf("failed to create structpb value: %v", err)
	}

	rr := &RuleResponse{
		Namespace: "ns",
		RuleName:  "authz.allow",
		Result:    structVal,
	}

	result := rr.String()
	if result == "" {
		t.Fatal("expected non-empty string")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("String() returned invalid JSON: %v", err)
	}

	resultMap, ok := parsed["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected result to be a map, got %T", parsed["result"])
	}
	if resultMap["allow"] != true {
		t.Errorf("expected allow=true, got %v", resultMap["allow"])
	}
	if resultMap["reason"] != "authorized" {
		t.Errorf("expected reason=%q, got %v", "authorized", resultMap["reason"])
	}
}
