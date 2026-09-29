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

package types

import (
	"encoding/json"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

func TestLedgerEventAnnotations_FunctionRequestLevel(t *testing.T) {
	req := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID: "req-123",
			NCAId:     "nca-001",
			FunctionDetails: function.Details{
				FunctionID:        "func-abc",
				FunctionVersionID: "fv-xyz",
			},
		},
	}

	got := LedgerEventAnnotations(req, "cluster-east", "us-east-1", nil)
	assert.Equal(t, map[string]string{
		LedgerAnnotationICMSRequestID:     "req-123",
		LedgerAnnotationNCAID:             "nca-001",
		LedgerAnnotationClusterID:         "cluster-east",
		LedgerAnnotationRegion:            "us-east-1",
		LedgerAnnotationFunctionID:        "func-abc",
		LedgerAnnotationFunctionVersionID: "fv-xyz",
	}, got)
	_, hasInstance := got[LedgerAnnotationInstanceID]
	assert.False(t, hasInstance, "request-level events must omit instance-id")
	_, hasTask := got[LedgerAnnotationTaskID]
	assert.False(t, hasTask)
}

func TestLedgerEventAnnotations_TaskIdentity(t *testing.T) {
	req := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID: "req-task",
			NCAId:     "nca-001",
			TaskDetails: task.Details{
				TaskID: "task-999",
			},
		},
	}

	got := LedgerEventAnnotations(req, "cluster-east", "", nil)
	assert.Equal(t, "task-999", got[LedgerAnnotationTaskID])
	_, hasFV := got[LedgerAnnotationFunctionVersionID]
	assert.False(t, hasFV, "tasks use task-id, not function-version-id")
	_, hasRegion := got[LedgerAnnotationRegion]
	assert.False(t, hasRegion, "empty fields omitted")
}

func TestLedgerEventAnnotations_InstanceStatusSubset(t *testing.T) {
	req := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID: "req-123",
			FunctionDetails: function.Details{
				FunctionVersionID: "fv-xyz",
			},
		},
	}
	update := &ICMSRequestUpdateInfo{
		InstanceID: "0-sr-abc",
		Payload: ICMSInstanceStatusUpdateRequest{
			InstanceState:    ICMSInstanceTerminated,
			Status:           ICMSRequestInstanceTerminatedByService,
			TerminationCause: ICMSInstanceFailedImagePullIssues,
			FailureCategory:  "image_pull",
		},
	}

	got := LedgerEventAnnotations(req, "cluster-east", "us-east-1", update)
	assert.Equal(t, "0-sr-abc", got[LedgerAnnotationInstanceID])
	assert.Equal(t, string(ICMSInstanceTerminated), got[LedgerAnnotationInstanceState])
	assert.Equal(t, string(ICMSRequestInstanceTerminatedByService), got[LedgerAnnotationStatus])
	assert.Equal(t, string(ICMSInstanceFailedImagePullIssues), got[LedgerAnnotationTerminationCause])
	assert.Equal(t, "image_pull", got[LedgerAnnotationFailureCategory])
}

func TestLedgerEventAnnotations_NilRequest(t *testing.T) {
	assert.Nil(t, LedgerEventAnnotations(nil, "c", "r", nil))
}

func TestICMSInstanceStatusUpdateRequest_FailureCategoryNotMarshaled(t *testing.T) {
	payload := ICMSInstanceStatusUpdateRequest{
		InstanceState:   ICMSInstanceTerminated,
		FailureCategory: "image_pull",
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "failureCategory")
	assert.NotContains(t, string(data), "image_pull")
	assert.Contains(t, string(data), "terminated")
}
