// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const maintenancePath = "../../.github/rulesets/maintenance-helm.json"

func maintenancePolicy(t *testing.T) Policy {
	t.Helper()
	data, err := os.ReadFile(maintenancePath)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := DecodePolicy(data)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func liveRelease() Object {
	var result Object
	_ = json.Unmarshal([]byte(`{
  "id":42,"name":"release","target":"branch","enforcement":"active",
  "source":"NVIDIA/nvcf","node_id":"read-only",
  "conditions":{"ref_name":{"include":["refs/heads/release-*","refs/heads/release-*/**/*"],"exclude":[]}},
  "bypass_actors":[{"actor_id":7,"actor_type":"Team","bypass_mode":"always"}],
  "rules":[
    {"type":"deletion"},{"type":"non_fast_forward"},
    {"type":"pull_request","parameters":{"required_approving_review_count":1,"require_code_owner_review":true}},
    {"type":"required_status_checks","parameters":{
      "strict_required_status_checks_policy":false,"do_not_enforce_on_create":true,
      "required_status_checks":[{"context":"docs","integration_id":15368},{"context":"bazel required checks","integration_id":15368}]
    }}
  ]
}`), &result)
	return result
}

// fakeRulesetAPI isolates write and race behavior from GitHub.
type fakeRulesetAPI struct {
	live        []Object
	writes      []Change
	reads       int
	beforeRead  func(*fakeRulesetAPI)
	afterWrite  func(*fakeRulesetAPI)
	snapshotErr error
	writeErr    error
}

// Snapshot records reads and allows tests to simulate concurrent changes.
func (f *fakeRulesetAPI) Snapshot(policy Policy) ([]Object, error) {
	f.reads++
	if f.beforeRead != nil {
		f.beforeRead(f)
	}
	if f.snapshotErr != nil {
		return nil, f.snapshotErr
	}
	var result []Object
	for _, current := range f.live {
		for _, desired := range policy.Rulesets {
			if current["name"] == desired["name"] {
				result = append(result, clone(current))
			}
		}
	}
	return result, nil
}

// Write records a successful update and supports readback-failure injection.
func (f *fakeRulesetAPI) Write(change Change) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writes = append(f.writes, clone(change))
	result := clone(change.After)
	result["id"] = float64(99)
	if change.ID != nil {
		result["id"] = float64(*change.ID)
	}
	var live []Object
	for _, current := range f.live {
		if current["name"] != change.Name {
			live = append(live, current)
		}
	}
	f.live = append(live, result)
	if f.afterWrite != nil {
		f.afterWrite(f)
	}
	return nil
}

func mustPlan(t *testing.T, policy Policy, live []Object) Plan {
	t.Helper()
	plan, err := MakePlan(policy, live)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func checkParameters(ruleset Object) Object {
	rules, _ := objects(ruleset["rules"])
	for _, rule := range rules {
		if rule["type"] == "required_status_checks" {
			parameters, _ := object(rule["parameters"])
			return parameters
		}
	}
	return nil
}

func requireError(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), text) {
		t.Fatalf("got %v, want error containing %q", err, text)
	}
}

func TestAllVersionedPoliciesAreValid(t *testing.T) {
	paths, err := filepath.Glob("../../.github/rulesets/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("policy files: %v", err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodePolicy(data); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMaintenancePlanChangesOnlyRequiredHelmCheck(t *testing.T) {
	live := []Object{liveRelease()}
	before := clone(live)
	plan := mustPlan(t, maintenancePolicy(t), live)
	if len(plan.Changes) != 1 {
		t.Fatalf("changes: %v", plan.Changes)
	}
	expected := Editable(before[0])
	parameters := checkParameters(expected)
	parameters["required_status_checks"] = append(parameters["required_status_checks"].([]any),
		Object{"context": "helm charts", "integration_id": float64(15368)})
	if !reflect.DeepEqual(plan.Changes[0].After, expected) || !reflect.DeepEqual(live, before) {
		t.Fatalf("plan changed unrelated settings or mutated input: %+v", plan)
	}
}

func TestExistingCheckFromWrongAppIsRebound(t *testing.T) {
	live := liveRelease()
	parameters := checkParameters(live)
	parameters["required_status_checks"] = append(parameters["required_status_checks"].([]any),
		Object{"context": "helm charts", "integration_id": nil})
	after := mustPlan(t, maintenancePolicy(t), []Object{live}).Changes[0].After
	checks, _ := objects(checkParameters(after)["required_status_checks"])
	if len(checks) != 3 || checks[2]["integration_id"] != float64(15368) {
		t.Fatalf("checks: %v", checks)
	}
}

func TestApplyPreservesProtectionsAndIsIdempotent(t *testing.T) {
	policy := maintenancePolicy(t)
	api := &fakeRulesetAPI{live: []Object{liveRelease()}}
	plan := mustPlan(t, policy, api.live)
	if err := ApplyPlan(policy, plan, api); err != nil {
		t.Fatal(err)
	}
	next := mustPlan(t, policy, api.live)
	if len(next.Changes) != 0 {
		t.Fatalf("second plan is not empty: %v", next)
	}
	if err := ApplyPlan(policy, next, api); err != nil {
		t.Fatal(err)
	}
	if len(api.writes) != 1 {
		t.Fatalf("writes: %d", len(api.writes))
	}
}

func TestMissingExistingRulesetIsNotRecreated(t *testing.T) {
	_, err := MakePlan(maintenancePolicy(t), nil)
	requireError(t, err, "required existing ruleset")
}

func TestScopeChangeFailsClosed(t *testing.T) {
	for _, field := range []string{"target", "conditions"} {
		t.Run(field, func(t *testing.T) {
			live := liveRelease()
			live[field] = "unexpected"
			_, err := MakePlan(maintenancePolicy(t), []Object{live})
			requireError(t, err, "review scope manually")
		})
	}
}

func TestDuplicateNamesAreRejected(t *testing.T) {
	policy := maintenancePolicy(t)
	_, err := MakePlan(policy, []Object{liveRelease(), liveRelease()})
	requireError(t, err, "duplicate ruleset names")
	policy.Rulesets = append(policy.Rulesets, clone(policy.Rulesets[0]))
	_, err = MakePlan(policy, []Object{liveRelease()})
	requireError(t, err, "names must be nonempty and unique")
}

func TestLiveChangeAfterReviewPreventsWrite(t *testing.T) {
	policy := maintenancePolicy(t)
	api := &fakeRulesetAPI{live: []Object{liveRelease()}}
	plan := mustPlan(t, policy, api.live)
	api.live[0]["bypass_actors"] = []any{}
	requireError(t, ApplyPlan(policy, plan, api), "changed since review")
	if len(api.writes) != 0 {
		t.Fatal("stale plan caused a write")
	}
}

func TestPolicyChangeAfterReviewPreventsWrite(t *testing.T) {
	policy := maintenancePolicy(t)
	api := &fakeRulesetAPI{live: []Object{liveRelease()}}
	plan := mustPlan(t, policy, api.live)
	policy.Rulesets[0]["bypass_actors"] = []any{}
	requireError(t, ApplyPlan(policy, plan, api), "changed since review")
	if len(api.writes) != 0 {
		t.Fatal("changed policy caused a write")
	}
}

func TestRaceBeforeWriteIsRejected(t *testing.T) {
	policy := maintenancePolicy(t)
	api := &fakeRulesetAPI{live: []Object{liveRelease()}}
	plan := mustPlan(t, policy, api.live)
	api.beforeRead = func(api *fakeRulesetAPI) {
		if api.reads == 2 {
			api.live[0]["bypass_actors"] = []any{}
		}
	}
	requireError(t, ApplyPlan(policy, plan, api), "changed before write")
	if len(api.writes) != 0 {
		t.Fatal("racing update caused a write")
	}
}

func TestRaceBeforeLaterWritePreservesFirstSuccess(t *testing.T) {
	policy := maintenancePolicy(t)
	second := clone(policy.Rulesets[0])
	second["name"] = "other release"
	policy.Rulesets = append(policy.Rulesets, second)
	other := liveRelease()
	other["name"], other["id"] = "other release", float64(43)
	api := &fakeRulesetAPI{live: []Object{liveRelease(), other}}
	plan := mustPlan(t, policy, api.live)
	api.beforeRead = func(api *fakeRulesetAPI) {
		if api.reads == 4 {
			api.live[0]["bypass_actors"] = []any{}
		}
	}
	requireError(t, ApplyPlan(policy, plan, api), "changed before write")
	if len(api.writes) != 1 || api.writes[0].Name != "release" {
		t.Fatalf("writes: %v", api.writes)
	}
}

func TestCheckIsReadOnlyAndReportsDrift(t *testing.T) {
	api := &fakeRulesetAPI{live: []Object{liveRelease()}}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--policy", maintenancePath, "check"}, &stdout, &stderr,
		func(string) RulesetAPI { return api })
	if code != 1 || len(api.writes) != 0 || stderr.Len() != 0 {
		t.Fatalf("code %d, writes %d, stderr %s", code, len(api.writes), stderr.String())
	}
}

func TestApplyRequiresReviewedPlan(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	code := run([]string{"--policy", maintenancePath, "apply"}, &stdout, &stderr,
		func(string) RulesetAPI { called = true; return nil })
	if code != 2 || called || !strings.Contains(stderr.String(), "requires --plan") {
		t.Fatalf("code %d, called %t, stderr %s", code, called, stderr.String())
	}
}

func TestReadbackDetectsLossOfExistingProtection(t *testing.T) {
	policy := maintenancePolicy(t)
	api := &fakeRulesetAPI{live: []Object{liveRelease()}, afterWrite: func(api *fakeRulesetAPI) {
		api.live[0]["rules"] = api.live[0]["rules"].([]any)[1:]
	}}
	requireError(t, ApplyPlan(policy, mustPlan(t, policy, api.live), api), "readback differs")
}

func TestReadbackAllowsDefaultsAndRuleReordering(t *testing.T) {
	expected := Editable(liveRelease())
	actual := clone(expected)
	rules := actual["rules"].([]any)
	rules[0], rules[1] = rules[1], rules[0]
	rules[0].(map[string]any)["server_default"] = true
	if !ExpectedReadback(expected, actual) {
		t.Fatal("harmless defaults/order rejected")
	}
	actual["bypass_actors"] = append(actual["bypass_actors"].([]any), Object{"actor_id": float64(8)})
	if ExpectedReadback(expected, actual) {
		t.Fatal("unexpected bypass actor accepted")
	}
}

func TestCreatePolicyAndExplicitEmptyBypassOwnership(t *testing.T) {
	policy := maintenancePolicy(t)
	policy.Rulesets[0]["require_existing"] = false
	policy.Rulesets[0]["bypass_actors"] = []any{}
	for _, live := range [][]Object{nil, {liveRelease()}} {
		api := &fakeRulesetAPI{live: live}
		plan := mustPlan(t, policy, api.live)
		if err := ApplyPlan(policy, plan, api); err != nil {
			t.Fatal(err)
		}
		if len(api.live[0]["bypass_actors"].([]any)) != 0 {
			t.Fatal("explicit bypass removal not applied")
		}
	}
}

func TestUnknownPolicyFieldIsRejected(t *testing.T) {
	policy := maintenancePolicy(t)
	policy.Rulesets[0]["enforcment"] = "active"
	_, err := MakePlan(policy, []Object{liveRelease()})
	requireError(t, err, "unknown policy field")
}

func TestForbiddenLiveRuleIsRejectedNotRemoved(t *testing.T) {
	policy := maintenancePolicy(t)
	policy.Rulesets[0]["forbidden_rule_types"] = []any{"creation"}
	live := liveRelease()
	live["rules"] = append(live["rules"].([]any), Object{"type": "creation"})
	_, err := MakePlan(policy, []Object{live})
	requireError(t, err, "conflicting rule")
	if len(live["rules"].([]any)) != 5 {
		t.Fatal("input was mutated")
	}
}

func TestPolicyCannotRequireForbiddenRule(t *testing.T) {
	policy := maintenancePolicy(t)
	policy.Rulesets[0]["forbidden_rule_types"] = []any{"required_status_checks"}
	_, err := MakePlan(policy, nil)
	requireError(t, err, "requires forbidden rule")
}

func TestSnapshotAndWriteFailuresStopApply(t *testing.T) {
	policy := maintenancePolicy(t)
	plan := mustPlan(t, policy, []Object{liveRelease()})
	for _, failing := range []string{"snapshot", "write"} {
		t.Run(failing, func(t *testing.T) {
			api := &fakeRulesetAPI{live: []Object{liveRelease()}}
			if failing == "snapshot" {
				api.snapshotErr = errors.New("snapshot failed")
			} else {
				api.writeErr = errors.New("write failed")
			}
			requireError(t, ApplyPlan(policy, plan, api), failing+" failed")
			if len(api.writes) != 0 {
				t.Fatal("failed operation recorded success")
			}
		})
	}
}

func TestPolicyRejectsTrailingJSON(t *testing.T) {
	data, err := os.ReadFile(maintenancePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecodePolicy(append(data, []byte("{}")...))
	requireError(t, err, "exactly one JSON document")
}

func TestMalformedExistingChecksAreNotDiscarded(t *testing.T) {
	live := liveRelease()
	checkParameters(live)["required_status_checks"] = "unexpected"
	_, err := MakePlan(maintenancePolicy(t), []Object{live})
	requireError(t, err, "existing required checks must be an array")
}
