// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// Object retains API fields not owned by a policy so updates preserve them.
type Object = map[string]any

// Policy describes selected repository protection requirements.
type Policy struct {
	Version    int      `json:"version"`
	Repository string   `json:"repository"`
	Rulesets   []Object `json:"rulesets"`
}

// Change contains the complete writable before/after state for one ruleset.
type Change struct {
	ID     *int64 `json:"id"`
	Name   string `json:"name"`
	Before Object `json:"before"`
	After  Object `json:"after"`
}

// Plan binds reviewed changes to a repository and policy content hash.
type Plan struct {
	Version      int      `json:"version"`
	Repository   string   `json:"repository"`
	PolicySHA256 string   `json:"policy_sha256"`
	Changes      []Change `json:"changes"`
}

var writableFields = []string{"name", "target", "enforcement", "conditions", "rules", "bypass_actors"}
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func clone[T any](value T) T {
	// Values originate in decoded JSON or the JSON-compatible types above.
	data, _ := json.Marshal(value)
	var result T
	_ = json.Unmarshal(data, &result)
	return result
}

func object(value any) (Object, error) {
	result, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected an object, got %T", value)
	}
	return result, nil
}

func objects(value any) ([]Object, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("expected an array, got %T", value)
	}
	result := make([]Object, 0, len(items))
	for _, item := range items {
		entry, err := object(item)
		if err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, nil
}

func stringsList(value any) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("expected a string array, got %T", value)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok || text == "" {
			return nil, fmt.Errorf("array entries must be nonempty strings")
		}
		result = append(result, text)
	}
	return result, nil
}

// DecodePolicy rejects unknown top-level fields and validates the policy.
func DecodePolicy(data []byte) (Policy, error) {
	var policy Policy
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return policy, fmt.Errorf("policy must contain exactly one JSON document")
	}
	return policy, ValidatePolicy(policy)
}

// ValidatePolicy checks the supported policy schema before accessing GitHub.
func ValidatePolicy(policy Policy) error {
	if policy.Version != 1 || !repositoryName.MatchString(policy.Repository) || len(policy.Rulesets) == 0 {
		return fmt.Errorf("policy requires version 1, repository OWNER/REPO, and rulesets")
	}
	names := map[string]bool{}
	for _, desired := range policy.Rulesets {
		name, ok := desired["name"].(string)
		if !ok || strings.TrimSpace(name) == "" || names[name] {
			return fmt.Errorf("ruleset names must be nonempty and unique")
		}
		names[name] = true
		if err := validateRuleset(desired); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func validateRuleset(desired Object) error {
	for key := range desired {
		if !slices.Contains(writableFields, key) && key != "require_existing" && key != "forbidden_rule_types" {
			return fmt.Errorf("unknown policy field %q", key)
		}
	}
	if desired["target"] != "branch" && desired["target"] != "tag" {
		return fmt.Errorf("target must be branch or tag")
	}
	if desired["enforcement"] != "active" {
		return fmt.Errorf("policy must require active enforcement")
	}
	conditions, err := object(desired["conditions"])
	if err != nil {
		return fmt.Errorf("conditions: %w", err)
	}
	ref, err := object(conditions["ref_name"])
	if err != nil {
		return fmt.Errorf("ref_name: %w", err)
	}
	include, err := stringsList(ref["include"])
	if err != nil || len(include) == 0 {
		return fmt.Errorf("explicit nonempty include ref patterns are required")
	}
	if _, err := stringsList(ref["exclude"]); err != nil {
		return fmt.Errorf("explicit exclude ref patterns are required")
	}
	if value, exists := desired["require_existing"]; exists {
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("require_existing must be a boolean")
		}
	}
	var forbidden []string
	if value, exists := desired["forbidden_rule_types"]; exists {
		forbidden, err = stringsList(value)
		if err != nil {
			return fmt.Errorf("forbidden_rule_types: %w", err)
		}
	}
	rules, err := objects(desired["rules"])
	if err != nil || len(rules) == 0 {
		return fmt.Errorf("at least one rule is required")
	}
	seen := map[string]bool{}
	for _, rule := range rules {
		kind, ok := rule["type"].(string)
		if !ok || kind == "" || seen[kind] {
			return fmt.Errorf("rule types must be nonempty and unique")
		}
		seen[kind] = true
		if slices.Contains(forbidden, kind) {
			return fmt.Errorf("policy requires forbidden rule %q", kind)
		}
		if kind == "required_status_checks" {
			parameters, err := object(rule["parameters"])
			if err != nil {
				return err
			}
			checks, err := objects(parameters["required_status_checks"])
			if err != nil || len(checks) == 0 {
				return fmt.Errorf("required checks must be a nonempty array")
			}
			contexts := map[string]bool{}
			for _, check := range checks {
				context, ok := check["context"].(string)
				if !ok || context == "" || contexts[context] {
					return fmt.Errorf("required check contexts must be nonempty and unique")
				}
				contexts[context] = true
			}
		}
	}
	return nil
}

// Editable keeps writable API fields and omits response-only metadata.
func Editable(ruleset Object) Object {
	result := Object{}
	for _, key := range writableFields {
		if value, exists := ruleset[key]; exists {
			result[key] = clone(value)
		}
	}
	return result
}

func mergeOwned(existing, desired Object) (Object, error) {
	result := clone(existing)
	for key, value := range desired {
		if key == "required_status_checks" {
			checks, err := mergeChecks(result[key], value)
			if err != nil {
				return nil, err
			}
			result[key] = checks
			continue
		}
		want, wantObject := value.(map[string]any)
		have, haveObject := result[key].(map[string]any)
		if wantObject && haveObject {
			merged, err := mergeOwned(have, want)
			if err != nil {
				return nil, err
			}
			result[key] = merged
		} else {
			result[key] = clone(value)
		}
	}
	return result, nil
}

func mergeChecks(existing, desired any) ([]any, error) {
	checks, ok := existing.([]any)
	if existing != nil && !ok {
		return nil, fmt.Errorf("existing required checks must be an array")
	}
	checks = clone(checks)
	wanted, err := objects(desired)
	if err != nil {
		return nil, err
	}
	for _, check := range wanted {
		match := -1
		for index, raw := range checks {
			current, err := object(raw)
			if err != nil {
				return nil, err
			}
			if current["context"] == check["context"] {
				if match != -1 {
					return nil, fmt.Errorf("ambiguous existing check %q", check["context"])
				}
				match = index
			}
		}
		if match == -1 {
			checks = append(checks, clone(check))
			continue
		}
		current, _ := object(checks[match])
		checks[match], err = mergeOwned(current, check)
		if err != nil {
			return nil, err
		}
	}
	return checks, nil
}

// DesiredPayload merges owned settings while rejecting unexpected scope changes.
func DesiredPayload(current, desired Object) (Object, error) {
	if current == nil {
		if desired["require_existing"] == true {
			return nil, fmt.Errorf("%s: required existing ruleset was not found", desired["name"])
		}
		payload := Editable(desired)
		if _, exists := payload["bypass_actors"]; !exists {
			payload["bypass_actors"] = []any{}
		}
		return payload, nil
	}
	for _, field := range []string{"target", "conditions"} {
		if !reflect.DeepEqual(current[field], desired[field]) {
			return nil, fmt.Errorf("%s: existing %s differs; review scope manually", desired["name"], field)
		}
	}
	rules, err := objects(current["rules"])
	if err != nil {
		return nil, err
	}
	forbidden, _ := stringsList(desired["forbidden_rule_types"])
	for _, rule := range rules {
		kind, _ := rule["type"].(string)
		if slices.Contains(forbidden, kind) {
			return nil, fmt.Errorf("%s: conflicting rule %q; review manually", desired["name"], kind)
		}
	}
	payload := Editable(current)
	payload["enforcement"] = desired["enforcement"]
	if value, exists := desired["bypass_actors"]; exists {
		payload["bypass_actors"] = clone(value)
	}
	result := payload["rules"].([]any)
	wanted, _ := objects(desired["rules"])
	for _, rule := range wanted {
		match := -1
		for index, existing := range rules {
			if existing["type"] == rule["type"] {
				if match != -1 {
					return nil, fmt.Errorf("%s: duplicate existing rule type %q", desired["name"], rule["type"])
				}
				match = index
			}
		}
		if match == -1 {
			result = append(result, clone(rule))
			continue
		}
		result[match], err = mergeOwned(rules[match], rule)
		if err != nil {
			return nil, err
		}
	}
	payload["rules"] = result
	return payload, nil
}

// MakePlan computes exact changes without mutating either input.
func MakePlan(policy Policy, live []Object) (Plan, error) {
	if err := ValidatePolicy(policy); err != nil {
		return Plan{}, err
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Version: 1, Repository: policy.Repository,
		PolicySHA256: fmt.Sprintf("%x", sha256.Sum256(encoded)), Changes: []Change{}}
	for _, desired := range policy.Rulesets {
		var current Object
		for _, ruleset := range live {
			if ruleset["name"] == desired["name"] {
				if current != nil {
					return Plan{}, fmt.Errorf("%s: ambiguous duplicate ruleset names", desired["name"])
				}
				current = ruleset
			}
		}
		after, err := DesiredPayload(current, desired)
		if err != nil {
			return Plan{}, err
		}
		var before Object
		var id *int64
		if current != nil {
			before = Editable(current)
			number, ok := current["id"].(float64)
			if !ok || number < 1 || number != float64(int64(number)) {
				return Plan{}, fmt.Errorf("%s: invalid existing ruleset ID", desired["name"])
			}
			value := int64(number)
			id = &value
		}
		if !reflect.DeepEqual(before, after) {
			plan.Changes = append(plan.Changes, Change{ID: id, Name: desired["name"].(string), Before: before, After: after})
		}
	}
	return plan, nil
}

// ExpectedReadback allows server-added object defaults, but not changed lists.
func ExpectedReadback(expected, actual any) bool {
	switch wanted := expected.(type) {
	case map[string]any:
		have, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range wanted {
			found, exists := have[key]
			if !exists || !ExpectedReadback(value, found) {
				return false
			}
		}
		return true
	case []any:
		have, ok := actual.([]any)
		if !ok || len(wanted) != len(have) {
			return false
		}
		used := make([]bool, len(have))
		for _, value := range wanted {
			matched := false
			for index, candidate := range have {
				if !used[index] && ExpectedReadback(value, candidate) {
					used[index], matched = true, true
					break
				}
			}
			if !matched {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(expected, actual)
	}
}
