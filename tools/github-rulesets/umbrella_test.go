// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// umbrellaRepository retains unrelated rulesets while exposing only selected names,
// matching the real GitHub snapshot endpoint used by the reconciler.
type umbrellaRepository struct {
	rulesets []Object
	writes   []Change
}

func (r *umbrellaRepository) Snapshot(policy Policy) ([]Object, error) {
	var selected []Object
	for _, rule := range r.rulesets {
		for _, wanted := range policy.Rulesets {
			if rule["name"] == wanted["name"] {
				selected = append(selected, clone(rule))
			}
		}
	}
	return selected, nil
}

func (r *umbrellaRepository) Write(change Change) error {
	r.writes = append(r.writes, clone(change))
	result := clone(change.After)
	if change.ID == nil {
		result["id"] = float64(100 + len(r.rulesets))
		r.rulesets = append(r.rulesets, result)
		return nil
	}
	result["id"] = float64(*change.ID)
	for index, rule := range r.rulesets {
		if rule["id"] == result["id"] {
			r.rulesets[index] = result
			return nil
		}
	}
	return os.ErrNotExist
}

func umbrellaPolicy(t *testing.T) Policy {
	t.Helper()
	data, err := os.ReadFile("../../.github/rulesets/umbrella-release.json")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := DecodePolicy(data)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func umbrellaObject(t *testing.T, raw string) Object {
	t.Helper()
	var object Object
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func frozenEditions(t *testing.T) Object {
	t.Helper()
	return umbrellaObject(t, `{
		"id":90,"name":"Frozen docs editions","target":"branch","enforcement":"active",
		"conditions":{"ref_name":{"include":["refs/heads/docs/releases/**"],"exclude":[]}},
		"bypass_actors":[],
		"rules":[{"type":"update"},{"type":"deletion"},{"type":"non_fast_forward"}]
	}`)
}

func umbrellaPlan(t *testing.T, policy Policy, repository *umbrellaRepository) Plan {
	t.Helper()
	live, err := repository.Snapshot(policy)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := MakePlan(policy, live)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestUmbrellaRolloutPreservesFrozenEditionsAndIsIdempotent(t *testing.T) {
	policy := umbrellaPolicy(t)
	frozen := frozenEditions(t)
	repository := &umbrellaRepository{rulesets: []Object{clone(frozen)}}
	plan := umbrellaPlan(t, policy, repository)
	if len(plan.Changes) != 2 || len(repository.writes) != 0 {
		t.Fatalf("planning should propose two creates without writes: %+v", plan)
	}
	if err := ApplyPlan(policy, plan, repository); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repository.rulesets[0], frozen) || len(repository.writes) != 2 {
		t.Fatalf("rollout changed unrelated rules: %+v", repository.rulesets)
	}
	plan = umbrellaPlan(t, policy, repository)
	if len(plan.Changes) != 0 {
		t.Fatalf("applied policy still has changes: %+v", plan)
	}
	if err := ApplyPlan(policy, plan, repository); err != nil {
		t.Fatal(err)
	}
	if len(repository.writes) != 2 {
		t.Fatal("reapplying an empty plan rewrote rulesets")
	}
}

func TestUmbrellaPartialRolloutPlansOnlyMissingRuleset(t *testing.T) {
	policy := umbrellaPolicy(t)
	repository := &umbrellaRepository{rulesets: []Object{frozenEditions(t)}}
	plan := umbrellaPlan(t, policy, repository)
	if err := repository.Write(plan.Changes[0]); err != nil {
		t.Fatal(err)
	}
	plan = umbrellaPlan(t, policy, repository)
	if len(plan.Changes) != 1 || plan.Changes[0].Name != "Maintained docs editions" {
		t.Fatalf("partial rollout should create only maintained docs rules: %+v", plan)
	}
	if err := ApplyPlan(policy, plan, repository); err != nil {
		t.Fatal(err)
	}
	if len(repository.rulesets) != 3 {
		t.Fatalf("expected frozen, product, and maintained rules, got %+v", repository.rulesets)
	}
}

func TestUmbrellaBootstrapAndReviewedDocsUpdatesRemainPossible(t *testing.T) {
	plan := umbrellaPlan(t, umbrellaPolicy(t), &umbrellaRepository{})
	tag, docs := plan.Changes[0].After, plan.Changes[1].After
	for _, tc := range []struct {
		name, target, scope string
		ruleset             Object
	}{
		{"product", "tag", "refs/tags/nvcf/**", tag},
		{"docs", "branch", "refs/heads/docs/versions/**", docs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wanted := Object{"ref_name": Object{"include": []string{tc.scope}, "exclude": []string{}}}
			if tc.ruleset["target"] != tc.target || !reflect.DeepEqual(tc.ruleset["conditions"], clone(wanted)) {
				t.Fatalf("policy affects unexpected scope: %+v", tc.ruleset)
			}
			if bypasses, ok := tc.ruleset["bypass_actors"].([]any); !ok || len(bypasses) != 0 {
				t.Fatal("policy permits bypass actors")
			}
		})
	}
	tagRules, _ := objects(tag["rules"])
	docsRules, _ := objects(docs["rules"])
	byType := func(rules []Object) map[string]Object {
		result := map[string]Object{}
		for _, rule := range rules {
			result[rule["type"].(string)] = rule
		}
		return result
	}
	tags, maintained := byType(tagRules), byType(docsRules)
	if tags["creation"] != nil || tags["update"] == nil || tags["deletion"] == nil {
		t.Fatalf("product tags must allow creation and reject mutation: %+v", tags)
	}
	if maintained["creation"] != nil || maintained["update"] != nil || maintained["deletion"] == nil || maintained["non_fast_forward"] == nil {
		t.Fatalf("maintained docs must allow reviewed updates, not force pushes: %+v", maintained)
	}
	reviews, _ := object(maintained["pull_request"]["parameters"])
	if approvals, ok := reviews["required_approving_review_count"].(float64); !ok || approvals < 1 {
		t.Fatal("maintained docs do not require an approval")
	}
	checks, _ := object(maintained["required_status_checks"]["parameters"])
	wanted := umbrellaObject(t, `{"checks":[{"context":"docs","integration_id":15368}]}`)
	if checks["do_not_enforce_on_create"] != true || !reflect.DeepEqual(checks["required_status_checks"], wanted["checks"]) {
		t.Fatalf("docs bootstrap or required check is incorrect: %+v", checks)
	}
}

func TestUmbrellaConflictingFreezesFailWithoutRemovingProtections(t *testing.T) {
	policy := umbrellaPolicy(t)
	for _, desired := range policy.Rulesets {
		forbidden, _ := stringsList(desired["forbidden_rule_types"])
		for _, kind := range forbidden {
			t.Run(desired["name"].(string)+"/"+kind, func(t *testing.T) {
				live, err := DesiredPayload(nil, desired)
				if err != nil {
					t.Fatal(err)
				}
				live["id"] = float64(100)
				live["rules"] = append(live["rules"].([]any), Object{"type": kind})
				before := clone(live)
				if _, err := MakePlan(policy, []Object{live}); err == nil {
					t.Fatal("conflicting freeze was accepted")
				}
				if !reflect.DeepEqual(live, before) {
					t.Fatal("planning changed existing protections")
				}
			})
		}
	}
}

func TestUmbrellaNewBypassAfterPlanRequiresAnotherReview(t *testing.T) {
	policy := umbrellaPolicy(t)
	repository := &umbrellaRepository{}
	if err := ApplyPlan(policy, umbrellaPlan(t, policy, repository), repository); err != nil {
		t.Fatal(err)
	}
	plan := umbrellaPlan(t, policy, repository)
	repository.rulesets[0]["bypass_actors"] = []any{Object{
		"actor_type": "RepositoryRole", "actor_id": float64(5), "bypass_mode": "always",
	}}
	before := clone(repository.rulesets)
	if err := ApplyPlan(policy, plan, repository); err == nil {
		t.Fatal("unreviewed bypass change was accepted")
	}
	if !reflect.DeepEqual(repository.rulesets, before) || len(repository.writes) != 2 {
		t.Fatal("stale-plan rejection changed repository state")
	}
}
