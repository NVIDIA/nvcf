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
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

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

func newLedgerTestCache(rec record.EventRecorder) *BackendK8sCache {
	return &BackendK8sCache{
		eventRecorder: rec,
		clusterName:   "cluster-east",
		clusterRegion: "us-east-1",
	}
}

// receiveEvent reads one event from the fake recorder, failing the test rather
// than blocking indefinitely if event emission regressed.
func receiveEvent(t *testing.T, rec *record.FakeRecorder) string {
	t.Helper()
	select {
	case ev := <-rec.Events:
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for an event to be recorded")
		return ""
	}
}

func functionLedgerRequest() *nvcav2beta1.ICMSRequest {
	return &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID: "req-1",
			NCAId:     "nca-1",
			FunctionDetails: function.Details{
				FunctionID:        "func-1",
				FunctionVersionID: "fv-1",
			},
		},
	}
}

func TestEmitICMSEventf_ForwardsAnnotationsAndArgs(t *testing.T) {
	rec := record.NewFakeRecorder(4)
	c := newLedgerTestCache(rec)

	c.EmitICMSEventf(functionLedgerRequest(), corev1.EventTypeNormal,
		"InstanceStatusUpdate", "%v is %v", instanceUpdate("0-sr-a"), "0-sr-a", "running")

	got := receiveEvent(t, rec)
	// Formatting args are forwarded to the recorder.
	assert.Contains(t, got, "Normal InstanceStatusUpdate 0-sr-a is running")
	// Instance-level annotations are stamped.
	assert.Contains(t, got, types.LedgerAnnotationInstanceID+":0-sr-a")
	assert.Contains(t, got, types.LedgerAnnotationICMSRequestID+":req-1")
	assert.Contains(t, got, types.LedgerAnnotationClusterID+":cluster-east")
	assert.Contains(t, got, types.LedgerAnnotationRegion+":us-east-1")
	assert.Empty(t, rec.Events, "exactly one event should be emitted")
}

func TestEmitICMSEvent_RequestLevelOmitsInstanceID(t *testing.T) {
	rec := record.NewFakeRecorder(4)
	c := newLedgerTestCache(rec)

	c.EmitICMSEvent(functionLedgerRequest(), corev1.EventTypeNormal,
		"InstanceCreation", "Request accepted for processing", nil)

	got := receiveEvent(t, rec)
	assert.Contains(t, got, "Normal InstanceCreation Request accepted for processing")
	assert.Contains(t, got, types.LedgerAnnotationICMSRequestID+":req-1")
	assert.NotContains(t, got, types.LedgerAnnotationInstanceID,
		"request-level events must not carry instance-id")
}

func TestEmitICMSEventf_NilGuards(t *testing.T) {
	tests := []struct {
		name string
		// newCache builds the cache under test. rec is non-nil only when the
		// case wires a fake recorder (so we can assert nothing was emitted).
		newCache func(rec record.EventRecorder) *BackendK8sCache
		withRec  bool
		req      *nvcav2beta1.ICMSRequest
	}{
		{
			name:     "nil cache",
			newCache: func(record.EventRecorder) *BackendK8sCache { return nil },
			req:      functionLedgerRequest(),
		},
		{
			name:     "nil recorder",
			newCache: func(record.EventRecorder) *BackendK8sCache { return &BackendK8sCache{clusterName: "cluster-east"} },
			req:      functionLedgerRequest(),
		},
		{
			name:     "nil request",
			newCache: newLedgerTestCache,
			withRec:  true,
			req:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rec *record.FakeRecorder
			var recorder record.EventRecorder
			if tt.withRec {
				rec = record.NewFakeRecorder(4)
				recorder = rec
			}
			c := tt.newCache(recorder)

			assert.NotPanics(t, func() {
				c.EmitICMSEventf(tt.req, corev1.EventTypeNormal, "R", "m", nil)
			})
			if rec != nil {
				assert.Empty(t, rec.Events, "no event should be emitted")
			}
		})
	}
}

func TestInstanceUpdate(t *testing.T) {
	assert.Nil(t, instanceUpdate(""), "empty instance-id yields a request-level (nil) update")

	got := instanceUpdate("0-sr-a")
	require.NotNil(t, got)
	assert.Equal(t, "0-sr-a", got.InstanceID)
}
