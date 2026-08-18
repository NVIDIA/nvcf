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

package nvca

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/stretchr/testify/assert"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestFailureCategoryAnnotationParity_ContainerImagePull(t *testing.T) {
	// Same mapping used at RecordWorkloadStatus call sites for container failures.
	cause := types.ICMSInstanceFailedImagePullIssues
	metricCategory := nvcametrics.ICMSInstanceStateToFailureCategory(cause)

	req := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID: "req-parity",
			FunctionDetails: function.Details{
				FunctionVersionID: "fv-1",
			},
		},
	}
	update := &types.ICMSRequestUpdateInfo{
		InstanceID: "0-sr-parity",
		Payload: types.ICMSInstanceStatusUpdateRequest{
			InstanceState:    types.ICMSInstanceTerminated,
			TerminationCause: cause,
			FailureCategory:  string(metricCategory),
		},
	}

	annotations := types.LedgerEventAnnotations(req, "cluster-east", "us-east-1", update)
	assert.Equal(t, string(metricCategory), annotations[types.LedgerAnnotationFailureCategory])
	assert.Equal(t, "image_pull", annotations[types.LedgerAnnotationFailureCategory])
}

func TestFailureCategoryAnnotationParity_HelmNotFound(t *testing.T) {
	cause := types.ICMSInstanceFailedNotFound
	metricCategory := nvcametrics.ICMSInstanceStateToFailureCategory(cause)

	req := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID: "req-helm",
			FunctionDetails: function.Details{
				FunctionVersionID: "fv-helm",
			},
		},
	}
	update := &types.ICMSRequestUpdateInfo{
		InstanceID: "sr-abc-miniservice",
		Payload: types.ICMSInstanceStatusUpdateRequest{
			InstanceState:    types.ICMSInstanceTerminated,
			TerminationCause: cause,
			FailureCategory:  string(metricCategory),
		},
	}

	annotations := types.LedgerEventAnnotations(req, "cluster-east", "us-east-1", update)
	assert.Equal(t, string(metricCategory), annotations[types.LedgerAnnotationFailureCategory])
	assert.Equal(t, "not_found", annotations[types.LedgerAnnotationFailureCategory])
}
