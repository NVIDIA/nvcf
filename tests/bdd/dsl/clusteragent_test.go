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
	"strings"
	"testing"
)

const selectedFunctionStatusOutput = `{
  "currentFunction": {"hasFunction": true, "functionId": "function-1", "versionId": "version-1"}
}`

func TestSelectedFunctionIdentityReturnsBothIDs(t *testing.T) {
	functionID, versionID, err := SelectedFunctionIdentity(selectedFunctionStatusOutput)
	if err != nil {
		t.Fatalf("selected function identity: %v", err)
	}
	if functionID != "function-1" || versionID != "version-1" {
		t.Fatalf("identity = %q/%q, want function-1/version-1", functionID, versionID)
	}
}

func TestSelectedFunctionIdentityFailsWhenNoFunctionSelected(t *testing.T) {
	if _, _, err := SelectedFunctionIdentity(`{"currentFunction": {"hasFunction": false}}`); err == nil {
		t.Fatal("expected error when no function is selected")
	}
}

func TestSelectedFunctionIdentityFailsOnEmptyIDs(t *testing.T) {
	cases := map[string]string{
		"empty function ID": `{"currentFunction": {"hasFunction": true, "functionId": "", "versionId": "version-1"}}`,
		"empty version ID":  `{"currentFunction": {"hasFunction": true, "functionId": "function-1", "versionId": "  "}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := SelectedFunctionIdentity(raw); err == nil {
				t.Fatal("expected error for an empty identity field")
			}
		})
	}
}

func TestSelectedFunctionIdentityParseError(t *testing.T) {
	if _, _, err := SelectedFunctionIdentity("not json"); err == nil {
		t.Fatal("expected parse error")
	}
}

// The compute-plane CLI lists only scheduled functions, so an idle function
// produces no row rather than a row reporting zero instances.
func TestScheduledFunctionInstancesAbsentAcceptsMissingRow(t *testing.T) {
	raw := `[{"functionId": "other", "functionVersionId": "other-version", "instanceCount": 3}]`
	if err := ScheduledFunctionInstancesAbsent(raw, "function-1", "version-1"); err != nil {
		t.Fatalf("absent instances: %v", err)
	}
}

func TestScheduledFunctionInstancesAbsentAcceptsZeroCountRow(t *testing.T) {
	raw := `[{"functionId": "function-1", "functionVersionId": "version-1", "instanceCount": 0}]`
	if err := ScheduledFunctionInstancesAbsent(raw, "function-1", "version-1"); err != nil {
		t.Fatalf("absent instances: %v", err)
	}
}

func TestScheduledFunctionInstancesAbsentFailsWhenInstancesScheduled(t *testing.T) {
	raw := `[{"functionId": "function-1", "functionVersionId": "version-1", "instanceCount": 2}]`
	err := ScheduledFunctionInstancesAbsent(raw, "function-1", "version-1")
	if err == nil {
		t.Fatal("expected error when instances are already scheduled")
	}
	if !strings.Contains(err.Error(), "reports 2 scheduled instances") {
		t.Fatalf("error = %v, want the reported instance count", err)
	}
}

// A row matching the function but not the version must not satisfy the
// assertion for the selected version.
func TestScheduledFunctionInstancesAbsentIgnoresOtherVersions(t *testing.T) {
	raw := `[{"functionId": "function-1", "functionVersionId": "version-2", "instanceCount": 5}]`
	if err := ScheduledFunctionInstancesAbsent(raw, "function-1", "version-1"); err != nil {
		t.Fatalf("absent instances: %v", err)
	}
}

func TestScheduledFunctionInstancesAbsentParseError(t *testing.T) {
	if err := ScheduledFunctionInstancesAbsent("not json", "function-1", "version-1"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestFunctionInstancesReadyMatchesCountAndStatus(t *testing.T) {
	raw := `{"instanceCount": 1, "instances": [{"id": "i-1", "status": "RUNNING"}]}`
	if err := FunctionInstancesReady(raw, 1, "running"); err != nil {
		t.Fatalf("instances ready: %v", err)
	}
}

func TestFunctionInstancesReadyFailsOnInstanceCount(t *testing.T) {
	raw := `{"instanceCount": 0, "instances": []}`
	err := FunctionInstancesReady(raw, 1, "running")
	if err == nil {
		t.Fatal("expected error when the instance count differs")
	}
	if !strings.Contains(err.Error(), "instance count = 0, want 1") {
		t.Fatalf("error = %v, want the observed and expected counts", err)
	}
}

// The compute-plane CLI derives instanceCount from the instances array, so this
// shape should not occur. A surplus entry must not read as readiness if it does.
func TestFunctionInstancesReadyFailsOnSurplusInstances(t *testing.T) {
	raw := `{"instanceCount": 1, "instances": [{"status": "running"}, {"status": "pending"}]}`
	err := FunctionInstancesReady(raw, 1, "running")
	if err == nil {
		t.Fatal("expected error when surplus instance entries exist")
	}
	if !strings.Contains(err.Error(), "instance entries count = 2, want 1") {
		t.Fatalf("error = %v, want instance entries count mismatch", err)
	}
}

func TestFunctionInstancesReadyFailsWhenStatusDiffers(t *testing.T) {
	raw := `{"instanceCount": 1, "instances": [{"id": "i-1", "status": "PENDING"}]}`
	err := FunctionInstancesReady(raw, 1, "running")
	if err == nil {
		t.Fatal("expected error when no instance reports the status")
	}
	if !strings.Contains(err.Error(), `0 instances report status "running"`) {
		t.Fatalf("error = %v, want the matched instance count", err)
	}
}

// The reported count can match while only some instances have reached the
// expected status; that is not readiness.
func TestFunctionInstancesReadyFailsOnPartialStatusMatch(t *testing.T) {
	raw := `{"instanceCount": 2, "instances": [{"status": "running"}, {"status": "pending"}]}`
	if err := FunctionInstancesReady(raw, 2, "running"); err == nil {
		t.Fatal("expected error when only some instances report the status")
	}
}

func TestFunctionInstancesReadyParseError(t *testing.T) {
	if err := FunctionInstancesReady("", 1, "running"); err == nil {
		t.Fatal("expected parse error")
	}
}
