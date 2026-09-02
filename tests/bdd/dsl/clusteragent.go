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
	"strings"
)

// selectedFunctionStatus is the subset of `nvcf-cli status --json` these
// helpers read. Unrelated status fields are ignored.
type selectedFunctionStatus struct {
	CurrentFunction struct {
		HasFunction bool   `json:"hasFunction"`
		FunctionID  string `json:"functionId"`
		VersionID   string `json:"versionId"`
	} `json:"currentFunction"`
}

// scheduledFunction is the subset of one `cluster agent list-functions --json`
// row these helpers read.
type scheduledFunction struct {
	FunctionID        string `json:"functionId"`
	FunctionVersionID string `json:"functionVersionId"`
	InstanceCount     int    `json:"instanceCount"`
}

// functionDetail is the subset of `cluster agent get-function --json` these
// helpers read.
type functionDetail struct {
	InstanceCount int `json:"instanceCount"`
	Instances     []struct {
		Status string `json:"status"`
	} `json:"instances"`
}

// SelectedFunctionIdentity returns the function and version IDs that
// `nvcf-cli status --json` reports as selected. It fails when no function is
// selected or when either ID is empty, so a caller never exports a blank
// identity into an environment variable.
func SelectedFunctionIdentity(raw string) (string, string, error) {
	var status selectedFunctionStatus
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		return "", "", fmt.Errorf("parse nvcf-cli status json: %w", err)
	}
	if !status.CurrentFunction.HasFunction {
		return "", "", fmt.Errorf("nvcf-cli reports no selected function")
	}
	functionID := strings.TrimSpace(status.CurrentFunction.FunctionID)
	versionID := strings.TrimSpace(status.CurrentFunction.VersionID)
	if functionID == "" {
		return "", "", fmt.Errorf("selected function has an empty function ID")
	}
	if versionID == "" {
		return "", "", fmt.Errorf("selected function has an empty version ID")
	}
	return functionID, versionID, nil
}

// ScheduledFunctionInstancesAbsent requires that `cluster agent list-functions
// --json` reports no instances for the identity. The compute-plane CLI lists
// only scheduled functions, so an unscheduled function produces no row at all;
// a missing row and a matching row reporting zero instances are both accepted.
func ScheduledFunctionInstancesAbsent(raw, functionID, versionID string) error {
	var functions []scheduledFunction
	if err := json.Unmarshal([]byte(raw), &functions); err != nil {
		return fmt.Errorf("parse cluster agent list-functions json: %w", err)
	}
	for _, function := range functions {
		if function.FunctionID != functionID || function.FunctionVersionID != versionID {
			continue
		}
		if function.InstanceCount != 0 {
			return fmt.Errorf(
				"function %s version %s reports %d scheduled instances, want 0",
				functionID, versionID, function.InstanceCount,
			)
		}
	}
	return nil
}

// FunctionInstancesReady requires `cluster agent get-function --json` to report
// exactly count instances and exactly count of them to carry status. The
// compute-plane CLI does not normalize instance status, so the comparison is
// case-insensitive. Surplus instance entries are rejected.
func FunctionInstancesReady(raw string, count int, status string) error {
	var detail functionDetail
	if err := json.Unmarshal([]byte(raw), &detail); err != nil {
		return fmt.Errorf("parse cluster agent get-function json: %w", err)
	}
	if detail.InstanceCount != count {
		return fmt.Errorf("instance count = %d, want %d", detail.InstanceCount, count)
	}
	if len(detail.Instances) != count {
		return fmt.Errorf("instance entries count = %d, want %d", len(detail.Instances), count)
	}
	expected := strings.ToLower(strings.TrimSpace(status))
	matched := 0
	for _, instance := range detail.Instances {
		if strings.ToLower(strings.TrimSpace(instance.Status)) == expected {
			matched++
		}
	}
	if matched != count {
		return fmt.Errorf("%d instances report status %q, want %d", matched, status, count)
	}
	return nil
}
