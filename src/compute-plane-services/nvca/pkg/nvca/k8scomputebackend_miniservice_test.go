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
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sigsyaml "sigs.k8s.io/yaml"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestCreateMiniService_Function(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })

	ctx, cancel := context.WithCancel(newTestContext())
	reg := prometheus.NewRegistry()
	ctx = nvcametrics.WithDefaultMetrics(ctx,
		"my-nca-id", "my-cluster", "my-cluster", "1.0.0",
		nvcametrics.WithEventErrorTotalDefaultEvents(append(getAgentEvents(), getNVCAMetricEvents()...)),
		nvcametrics.WithContainerCrashAndRestartTotalDefaultContainerNames(GetDefaultWorkloadContainerNamesToWatch()),
		nvcametrics.WithRegisterer(reg))
	t.Cleanup(cancel)

	inferenceTestataDir := filepath.Join("testdata", "rendered-inference-test")
	ncaID := "ncaID"
	reqID := "reqID"
	msgID := "msgID"
	miniServiceNamespace := "sr-" + reqID

	objs, _ := decodeInferenceManifests(t, inferenceTestataDir, miniServiceNamespace)

	// Create the objects that would be created by the MiniService controller.
	clients := mockKubeClients(objs...)
	// Create a mock feature flag fetcher with AckTaskRequestAfterPodsScheduled enabled
	featureFlagFetcher := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{featureflag.AckTaskRequestAfterPodsScheduled},
	}

	bc, _, err := NewBackendk8sCacheBuilder().
		WithClients(clients).
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(10).
		WithFeatureFlagFetcher(featureFlagFetcher).
		Start(ctx)
	require.NoError(t, err)

	srClient := clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace)

	createMsg := function.CreationQueueMessage{
		Details: function.Details{
			FunctionID:        "funcid",
			FunctionVersionID: "funcverid",
			FunctionType:      "DEFAULT",
		},
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			RequestID:     reqID,
			NCAID:         ncaID,
			Action:        common.FunctionCreationAction,
			InstanceType:  "DGXCLOUD.GPU.A100",
			InstanceCount: 1,
		},
		LaunchSpecification: &function.LaunchSpecification{
			CloudProvider:   "DGXCLOUD",
			ICMSEnvironment: "prod",
			GPUName:         "L40",
			EnvironmentB64:  "QVRUQUNIRURfR1BVX0NPVU5UPSIxIgpCWU9PX09URUxfQ09MTEVDVE9SX0NPTlRBSU5FUj1udmNyLmlvL3F0ZnB0MWgwYmlldS9udmNmLWNvcmUvYnlvby1vdGVsLWNvbGxlY3RvcjoxLjIuMwpDTE9VRF9QUk9WSURFUj1PTi1QUkVNCkVTU19BR0VOVF9DT05UQUlORVI9bnZjci5pby9udi1jZi9udmNmLWNvcmUvZXNzLWFnZW50OjEuMC4wCkZVTkNUSU9OX0lEPTVhM2Q0YTdlLTllZTMtNDc2Mi04ZDM3LWQzYjQwYTZmODRjNgpGVU5DVElPTl9OQU1FPW15LWZ1bmMKRlVOQ1RJT05fVkVSU0lPTl9JRD0yYzk0OGQ5Yi1kYjVkLTRmOTMtOGMyOS1mNWQ4YTVkODljYjkKR1BVX05BTUU9TDQwCkhFTE1fQ0hBUlRfSU5GRVJFTkNFX1NFUlZJQ0VfTkFNRT1teXNlcnZpY2UKSU5GRVJFTkNFX0NPTlRBSU5FUl9DUkVERU5USUFMPWNvbnRhaW5lci1jcmVkLTEKSU5GRVJFTkNFX0NPTlRBSU5FUl9FTlY9VzNzaWEyVjVJam9pU1U1R1JWSkZUa05GWDBWT1ZsOUxSVmtpTENKMllXeDFaU0k2SW1sdVptVnlaVzVqWlY5MllXeDFaU0o5WFE9PQpJTkZFUkVOQ0VfSEVBTFRIX0VORFBPSU5UPS92Mi9oZWFsdGgvcmVhZHkKSU5GRVJFTkNFX0hFQUxUSF9FWFBFQ1RFRF9SRVNQT05TRV9DT0RFPSIyMDAiCklORkVSRU5DRV9IRUFMVEhfUE9SVD0iNTAwNTEiCklORkVSRU5DRV9QT1JUPSI1MDA1MSIKSU5GRVJFTkNFX1BST1RPQ09MPUdSUEMKSU5GRVJFTkNFX1VSTD0vZ3JwYwpJTklUX0NPTlRBSU5FUj1udmNyLmlvL3F0ZnB0MWgwYmlldS9udmNmLWNvcmUvbnZjZl93b3JrZXJfaW5pdDowLjI0LjEwCk1BWF9SRVFVRVNUX0NPTkNVUlJFTkNZPSIxIgpOQ0FfSUQ9X2xJTFhCLTFOZk5tQm5RU2tfc3BxVldPdENBWFFtNTBVRU13ajNUUmd5bUpKMkF5dXdjZ3hxCk5WQ0ZfRlFETj1odHRwczovL3VzLXdlc3QtMi5hcGkubnZjZi5udmlkaWEuY29tCk5WQ0ZfRlFETl9HUlBDPWh0dHBzOi8vZ3JwYy5hcGkubnZjZi5udmlkaWEuY29tCk5WQ0ZfRlFETl9OQVRTPXRsczovL3VzLXdlc3QtMi5hd3MuY2xvdWQubmF0cy5udmNmLm52aWRpYS5jb206NDIyMgpOVkNGX1dPUktFUl9UT0tFTj10b2sKT1RFTF9DT05UQUlORVI9bnZjci5pby9xdGZwdDFoMGJpZXUvbnZjZi1jb3JlL29wZW50ZWxlbWV0cnktY29sbGVjdG9yOjAuNzQuMApPVEVMX0VYUE9SVEVSX09UTFBfRU5EUE9JTlQ9aHR0cHM6Ly9wcm9kLm90ZWwua2FpemVuLm52aWRpYS5jb206ODI4MgpTRUNSRVRTX0FTU0VSVElPTl9UT0tFTj1leUpoYkdjaU9pSlNVekkxTmlJc0luUjVjQ0k2SWtwWFZDSjkuZXlKemRXSWlPaUl4TWpNME5UWTNPRGt3SWl3aWJtRnRaU0k2SWtwdmFHNGdSRzlsSWl3aVlYTnpaWEowYVc5dUlqcDdJbk5sWTNKbGRGQmhkR2h6SWpwYkltRmpZMjkxYm5SekwxOXNTVXhZUWkweFRtWk9iVUp1VVZOclgzTndjVlpYVDNSRFFWaFJiVFV3VlVWTmQyb3pWRkpuZVcxS1NqSkJlWFYzWTJkNGNTOTBaV3hsYldWMGNua3ZObVpoT0RNMk5tVXROMkpoTWkwME1UUXpMV0UzTkRRdE56VXhOV1poWmpsaVpqY3lJaXdpWVdOamIzVnVkSE12WDJ4SlRGaENMVEZPWms1dFFtNVJVMnRmYzNCeFZsZFBkRU5CV0ZGdE5UQlZSVTEzYWpOVVVtZDViVXBLTWtGNWRYZGpaM2h4TDNSbGJHVnRaWFJ5ZVM4MlptRTRNelkyWlMwM1ltRXlMVFF4TkRNdFlUYzBOQzAzTlRFMVptRm1PV0ptT0RFaVhYMHNJbUZrYldsdUlqcDBjblZsTENKcFlYUWlPakUxTVRZeU16a3dNako5LlNwUVliRmUxbmZyUTVLc2hSbHk5U1VDMjZXX2oycFFoNkRNaW5zYnJzUUh2S2cxc2Uyb0gzVnpvaW5iTWJRel81TFhjZy1YTmt4NGNOSk4yQWp1d1VJems2RElVTElDSGVxdWpxLXhBYWdGUjhfejI1bzExZDAxekJTNU54RjlBQ2d0SWw2OWRoVEhrOHNLMmVRYjRBRkdDRmZmNjFqMGtYYWJJWUVTR0p4ZHY5UmtOZld0WVotRm1JYzl1RjRqWTU5elIxRUJkWGlsY2NjUjBSaUN2S0FsVFlvckU3VGotMDRLZ1RGbnZRYm1QMFRRR1FkNnhicWRBYVBSQnBYeUJHMDRxbUEyOTZUZnJBT1ZfMDJhSWR0akhhNVNqbXZ0UEFiVmVIVlY1QnhfWmQ4eVZteU4wZTdxZWduQU9xYzVOUDNrRjM4VzRuV2hURThWa05UWmpUUEEKU0lERUNBUl9DUkVERU5USUFMPWNvbnRhaW5lci1jcmVkLTEKVFJBQ0lOR19BQ0NFU1NfVE9LRU49dHJhY2UtdG9rLTEKVVRJTFNfQ09OVEFJTkVSPW52Y3IuaW8vcXRmcHQxaDBiaWV1L252Y2YtY29yZS9udmNmX3dvcmtlcl91dGlsczoyLjIxLjQK",
			HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
				HelmChartURL: "https://helm.ngc.nvidia.com/qtfpt1h0bieu/byoc/charts/testchart-0.1.0.tgz",
				Values:       []byte(`{"foo":{"bar":"baz"}}`),
			},
		},
	}
	sr, err := bc.CreateICMSCreationMessageRequest(ctx, createMsg, "receipt1", msgID, "")
	require.NoError(t, err)
	assert.NotNil(t, sr)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	req, err := srClient.Get(ctx, "sr-"+reqID, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, req.Status.Instances, 0)

	// Mimic Agent.PutICMSRequestAcknowledgement()
	applied := bc.applyICMSRequestStatusChange(ctx, sr.DeepCopy(), func(_ context.Context, s *nvcav2beta1.ICMSRequest) {
		s.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusPending
	})
	require.True(t, applied)

	srName := sr.Name
	instanceID := srName + "-miniservice"

	// Wait for lister to see the status update before syncing (may already be InProgress if workers are fast)
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		sr, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).Get(srName)
		if assert.NoError(ct, err) {
			assert.NotEmpty(ct, sr.Status.RequestStatus)
		}
	}, 5*time.Second, 50*time.Millisecond)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err = bc.SyncAllICMSRequests(ctx)
		assert.NoError(ct, err)
		bc.ForceSync(ctx)
		req, err := srClient.Get(ctx, srName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		assert.Len(ct, req.Status.Instances, 1)
		assert.Contains(ct, req.Status.Instances, instanceID)
		assert.Equal(ct, nvcav2beta1.ICMSRequestStatusInProgress, req.Status.RequestStatus)
	}, 30*time.Second, 100*time.Millisecond)

	req, err = srClient.Get(ctx, srName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Contains(t, req.Status.Instances, instanceID)
	statusForInstanceID := req.Status.Instances[instanceID]
	assert.Equal(t, nvcav2beta1.InstanceStatus{
		ID:                    instanceID,
		Type:                  nvcav2beta1.InstanceTypeMiniService,
		Status:                string(nvcatypes.ICMSInstanceStarted),
		LastReportedStatus:    string(nvcatypes.ICMSInstanceStateNoStatus),
		LastReportedTimestamp: nil,
	}, statusForInstanceID)

	// No phase on miniservice yet, should be an empty status update.
	up, err := bc.k8sArtifactHelper.(K8sComputeBackend).GetICMSRequestUpdatesForMiniServiceRequest(ctx, req, req.Status.Instances[instanceID])
	require.NoError(t, err)
	assert.Empty(t, up)

	ms := &v1alpha1.MiniService{}
	ms.Name = instanceID
	err = clients.HelmV2.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	svcPort := int32(50051)
	require.Equal(t, &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "sr-reqID-miniservice",
			ResourceVersion: "1",
			Labels: map[string]string{
				nvcatypes.FunctionIDKey:             "funcid",
				nvcatypes.FunctionIDUpperKey:        "funcid",
				nvcatypes.FunctionVersionIDKey:      "funcverid",
				nvcatypes.FunctionVersionIDUpperKey: "funcverid",
				"gpu-name":                          "",
				nvcatypes.ICMSRequestIDKey:          "reqID",
				nvcatypes.NCAIDKey:                  "nca-ncaID-nca",
				nvcatypes.NCAIDUpperKey:             "nca-ncaID-nca",
				"nvcf.nvidia.io/message-batch-id":   "",
			},
			Annotations: map[string]string{
				"instance-count":           "1",
				nvcatypes.ICMSRequestIDKey: "reqID",
				nvcatypes.NCAIDKey:         "ncaID",
				nvcatypes.ClusterGroupKey:  "",
			},
		},
		Spec: v1alpha1.MiniServiceSpec{
			Namespace:       req.Name,
			ICMSRequestName: req.Name,
			HelmChartConfig: common.HelmConfig{
				URL:         "https://helm.ngc.nvidia.com/qtfpt1h0bieu/byoc/charts/testchart-0.1.0.tgz",
				ServiceName: "myservice",
				ServicePort: &svcPort,
				Values:      createMsg.LaunchSpecification.Values,
			},
		},
	}, ms)

	ms.Status.Phase = v1alpha1.MiniServiceInstalling
	err = clients.HelmV2.Status().Update(ctx, ms)
	require.NoError(t, err)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		bc.ForceSync(ctx)
		req, err := srClient.Get(ctx, srName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		if assert.Len(ct, req.Status.Instances, 1) {
			assert.Equal(ct, nvcav2beta1.InstanceStatus{
				ID:     instanceID,
				Type:   nvcav2beta1.InstanceTypeMiniService,
				Status: string(nvcatypes.ICMSInstanceStarted),
			}, req.Status.Instances[instanceID])
		}
		assert.Equal(ct, nvcav2beta1.ICMSRequestStatusInProgress, req.Status.RequestStatus)
	}, 5*time.Second, 100*time.Millisecond)

	ms.Status.Phase = v1alpha1.MiniServiceStarting
	err = clients.HelmV2.Status().Update(ctx, ms)
	require.NoError(t, err)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		bc.ForceSync(ctx)
		req, err := srClient.Get(ctx, srName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		if assert.Len(ct, req.Status.Instances, 1) {
			assert.Equal(ct, nvcav2beta1.InstanceStatus{
				ID:     instanceID,
				Type:   nvcav2beta1.InstanceTypeMiniService,
				Status: string(nvcatypes.ICMSInstanceStarted),
			}, req.Status.Instances[instanceID])
		}
		assert.Equal(ct, nvcav2beta1.ICMSRequestStatusInstancesInProgress, req.Status.RequestStatus)
	}, 5*time.Second, 100*time.Millisecond)

	ms.Status.Phase = v1alpha1.MiniServiceFailed
	ms.Status.Conditions = []metav1.Condition{
		{
			Reason:  v1alpha1.MiniServiceStatusReasonObjectsFailed,
			Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
			Status:  metav1.ConditionFalse,
			Message: "v1.Pod pod:\t\nsome issue",
		},
	}
	err = clients.HelmV2.Status().Update(ctx, ms)
	require.NoError(t, err)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		bc.ForceSync(ctx)
		req, err := srClient.Get(ctx, srName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		if assert.Len(ct, req.Status.Instances, 1) {
			assert.Equal(ct, nvcav2beta1.InstanceStatus{
				ID:     instanceID,
				Type:   nvcav2beta1.InstanceTypeMiniService,
				Status: string(nvcatypes.ICMSInstanceStarted),
			}, req.Status.Instances[instanceID])
		}
		assert.Equal(ct, nvcav2beta1.ICMSRequestStatusFailed, req.Status.RequestStatus)
	}, 5*time.Second, 100*time.Millisecond)

	req, err = srClient.Get(ctx, srName, metav1.GetOptions{})
	require.NoError(t, err)
	up, err = bc.k8sArtifactHelper.(K8sComputeBackend).GetICMSRequestUpdatesForMiniServiceRequest(ctx, req, req.Status.Instances[instanceID])
	require.NoError(t, err)
	assert.Equal(t, up.InstanceID, instanceID)
	assert.Equal(t, nvcatypes.ICMSInstanceRequestClosed, up.Payload.RequestState)
}

func TestCreateMiniService_TaskDelayedAck(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })

	ctx, cancel := context.WithCancel(newTestContext())
	reg := prometheus.NewRegistry()
	ctx = nvcametrics.WithDefaultMetrics(ctx,
		"my-nca-id", "my-cluster", "my-cluster", "1.0.0",
		nvcametrics.WithEventErrorTotalDefaultEvents(append(getAgentEvents(), getNVCAMetricEvents()...)),
		nvcametrics.WithContainerCrashAndRestartTotalDefaultContainerNames(GetDefaultWorkloadContainerNamesToWatch()),
		nvcametrics.WithRegisterer(reg))
	t.Cleanup(cancel)

	inferenceTestataDir := filepath.Join("testdata", "rendered-inference-test")
	ncaID := "ncaID"
	reqID := "reqID"
	msgID := "msgID"
	miniServiceNamespace := "sr-" + reqID

	objs, _ := decodeInferenceManifests(t, inferenceTestataDir, miniServiceNamespace)

	// Create the objects that would be created by the MiniService controller.
	clients := mockKubeClients(objs...)
	// Create a mock feature flag fetcher with AckTaskRequestAfterPodsScheduled enabled
	featureFlagFetcher := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{featureflag.AckTaskRequestAfterPodsScheduled},
	}

	bc, _, err := NewBackendk8sCacheBuilder().
		WithClients(clients).
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(10).
		WithFeatureFlagFetcher(featureFlagFetcher).
		Start(ctx)
	require.NoError(t, err)

	srClient := clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace)

	createMsg := task.CreationQueueMessage{
		Details: task.Details{
			TaskID:   "taskid",
			TaskType: "HELMCHART",
		},
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			RequestID:     reqID,
			NCAID:         ncaID,
			Action:        common.TaskCreationAction,
			InstanceType:  "DGXCLOUD.GPU.A100",
			InstanceCount: 1,
		},
		LaunchSpecification: task.LaunchSpecification{
			CloudProvider:                  "DGXCLOUD",
			ICMSEnvironment:                "prod",
			ResultHandlingStrategy:         "UPLOAD",
			TerminationGracePeriodDuration: "PT10M",
			MaxRuntimeDuration:             "PT2H",
			MaxQueuedDuration:              "PT2M",
			EnvironmentB64:                 "RVNTX0FHRU5UX0NPTlRBSU5FUj1zdGcubnZjci5pby9udi1jZi9udmNmLWNvcmUvZXNzLWFnZW50OjEuMC4wCklOSVRfQ09OVEFJTkVSPW52Y3IuaW8vcXRmcHQxaDBiaWV1L252Y2YtY29yZS9udmNmX3dvcmtlcl9pbml0OjAuMjQuMTAKTUFYX1JFUVVFU1RfQ09OQ1VSUkVOQ1k9IjEiCk5DQV9JRD1fbElMWEItMU5mTm1CblFTa19zcHFWV090Q0FYUW01MFVFTXdqM1RSZ3ltSkoyQXl1d2NneHEKTlZDRl9GUUROPWh0dHBzOi8vdXMtd2VzdC0yLmFwaS5udmNmLm52aWRpYS5jb20KTlZDRl9GUUROX0dSUEM9aHR0cHM6Ly9ncnBjLmFwaS5udmNmLm52aWRpYS5jb20KTlZDRl9GUUROX05BVFM9dGxzOi8vdXMtd2VzdC0yLmF3cy5jbG91ZC5uYXRzLm52Y2YubnZpZGlhLmNvbTo0MjIyCk5WQ0ZfV09SS0VSX1RPS0VOPXRvawpPVEVMX0NPTlRBSU5FUj1udmNyLmlvL3F0ZnB0MWgwYmlldS9udmNmLWNvcmUvb3BlbnRlbGVtZXRyeS1jb2xsZWN0b3I6MC43NC4wCk9URUxfRVhQT1JURVJfT1RMUF9FTkRQT0lOVD1odHRwczovL3Byb2Qub3RlbC5rYWl6ZW4ubnZpZGlhLmNvbTo4MjgyClNFQ1JFVFNfQVNTRVJUSU9OX1RPS0VOPWV5SmhiR2NpT2lKU1V6STFOaUlzSW5SNWNDSTZJa3BYVkNKOS5leUp6ZFdJaU9pSXhNak0wTlRZM09Ea3dJaXdpYm1GdFpTSTZJa3B2YUc0Z1JHOWxJaXdpWVhOelpYSjBhVzl1SWpwN0luTmxZM0psZEZCaGRHaHpJanBiSW1GalkyOTFiblJ6TDE5c1NVeFlRaTB4VG1aT2JVSnVVVk5yWDNOd2NWWlhUM1JEUVZoUmJUVXdWVVZOZDJvelZGSm5lVzFLU2pKQmVYVjNZMmQ0Y1M5MFpXeGxiV1YwY25rdk5tWmhPRE0yTm1VdE4ySmhNaTAwTVRRekxXRTNORFF0TnpVeE5XWmhaamxpWmpjeUlpd2lZV05qYjNWdWRITXZYMnhKVEZoQ0xURk9aazV0UW01UlUydGZjM0J4VmxkUGRFTkJXRkZ0TlRCVlJVMTNhak5VVW1kNWJVcEtNa0Y1ZFhkalozaHhMM1JsYkdWdFpYUnllUzgyWm1FNE16WTJaUzAzWW1FeUxUUXhORE10WVRjME5DMDNOVEUxWm1GbU9XSm1PREVpWFgwc0ltRmtiV2x1SWpwMGNuVmxMQ0pwWVhRaU9qRTFNVFl5TXprd01qSjkuU3BRWWJGZTFuZnJRNUtzaFJseTlTVUMyNldfajJwUWg2RE1pbnNicnNRSHZLZzFzZTJvSDNWem9pbmJNYlF6XzVMWGNnLVhOa3g0Y05KTjJBanV3VUl6azZESVVMSUNIZXF1anEteEFhZ0ZSOF96MjVvMTFkMDF6QlM1TnhGOUFDZ3RJbDY5ZGhUSGs4c0syZVFiNEFGR0NGZmY2MWowa1hhYklZRVNHSnhkdjlSa05mV3RZWi1GbUljOXVGNGpZNTl6UjFFQmRYaWxjY2NSMFJpQ3ZLQWxUWW9yRTdUai0wNEtnVEZudlFibVAwVFFHUWQ2eGJxZEFhUFJCcFh5QkcwNHFtQTI5NlRmckFPVl8wMmFJZHRqSGE1U2ptdnRQQWJWZUhWVjVCeF9aZDh5Vm15TjBlN3FlZ25BT3FjNU5QM2tGMzhXNG5XaFRFOFZrTlRaalRQQQpTSURFQ0FSX0NSRURFTlRJQUw9Y29udGFpbmVyLWNyZWQtMQpUQVNLX0NPTlRBSU5FUj1udmNyLmlvL215b3JnL2dwdC0zLjUtdHVyYm8tZmluZS10dW5lOjEuMC4wClRBU0tfQ09OVEFJTkVSX0FSR1M9LWFyZzE9dGVzdDEgYXJnMj10ZXN0MgpUQVNLX0NPTlRBSU5FUl9DUkVERU5USUFMPWNvbnRhaW5lci1jcmVkLTEKVEFTS19DT05UQUlORVJfRU5WPVczc2lhMlY1SWpvaVZFRlRTMTlGVGxaZlMwVlpJaXdpZG1Gc2RXVWlPaUowWVhOclgzWmhiSFZsSW4xZApUQVNLX0hFQUxUSF9FTkRQT0lOVD0vdjIvaGVhbHRoL3JlYWR5ClRBU0tfSEVBTFRIX0VYUEVDVEVEX1JFU1BPTlNFX0NPREU9IjIwMCIKVEFTS19IRUFMVEhfUE9SVD0iNTAwNTEiClRBU0tfSUQ9MTNlMmI1OTktOTZjYS00MmI1LWE0MTktOGZhN2Y3MDFkNWQyClRBU0tfTkFNRT1teS10YXNrClRBU0tfUE9SVD0iNTAwNTEiClRBU0tfUFJPVE9DT0w9R1JQQwpUQVNLX1NFQ1JFVFNfUFJFU0VOVD10cnVlClRBU0tfVVJMPS9ncnBjClRFUk1JTkFUSU9OX0dSQUNFX1BFUklPRD1QVDJIClRSQUNJTkdfQUNDRVNTX1RPS0VOPXRyYWNlLXRvay0xClVUSUxTX0NPTlRBSU5FUj1udmNyLmlvL3F0ZnB0MWgwYmlldS9udmNmLWNvcmUvbnZjZl93b3JrZXJfdXRpbHM6Mi4yMS40Cg==",
			HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
				HelmChartURL: "https://helm.ngc.nvidia.com/qtfpt1h0bieu/byoc/charts/testchart-0.1.0.tgz",
				Values:       []byte(`{"foo":{"bar":"baz"}}`),
			},
		},
	}
	sr, err := bc.CreateICMSCreationMessageRequest(ctx, createMsg, "receipt1", msgID, "")
	require.NoError(t, err)
	assert.NotNil(t, sr)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	req, err := srClient.Get(ctx, "sr-"+reqID, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, req.Status.Instances, 0)

	// Mimic Agent.PutICMSRequestAcknowledgement()
	applied := bc.applyICMSRequestStatusChange(ctx, req.DeepCopy(), func(_ context.Context, s *nvcav2beta1.ICMSRequest) {
		s.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusPending
	})
	require.True(t, applied)

	srName := req.Name
	instanceID := req.Name + "-miniservice"

	// Force sync to ensure the status update is propagated to the lister
	bc.ForceSync(ctx)

	// Wait for both API and lister to see the status update before syncing (may already be InProgress if workers are fast)
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		// Check API directly
		reqAPI, err := srClient.Get(ctx, srName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		if !assert.NotEmpty(ct, reqAPI.Status.RequestStatus, "status should be set in API") {
			return
		}
		// Check lister
		sr, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).Get(srName)
		if assert.NoError(ct, err) {
			assert.NotEmpty(ct, sr.Status.RequestStatus, "status should be set in lister")
		}
	}, 5*time.Second, 50*time.Millisecond)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err = bc.SyncAllICMSRequests(ctx)
		assert.NoError(ct, err)
		bc.ForceSync(ctx)
		req, err := srClient.Get(ctx, srName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		assert.Len(ct, req.Status.Instances, 1)
		assert.Contains(ct, req.Status.Instances, instanceID)
		assert.Equal(ct, nvcav2beta1.ICMSRequestStatusInProgress, req.Status.RequestStatus)
	}, 15*time.Second, 100*time.Millisecond)

	req, err = srClient.Get(ctx, srName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Contains(t, req.Status.Instances, instanceID)
	statusForInstanceID := req.Status.Instances[instanceID]
	assert.Equal(t, nvcav2beta1.InstanceStatus{
		ID:                    instanceID,
		Type:                  nvcav2beta1.InstanceTypeMiniService,
		Status:                string(nvcatypes.ICMSInstanceStarted),
		LastReportedStatus:    string(nvcatypes.ICMSInstanceStateNoStatus),
		LastReportedTimestamp: nil,
	}, statusForInstanceID)

	// No phase on miniservice yet, should be an empty status update.
	up, err := bc.k8sArtifactHelper.(K8sComputeBackend).GetICMSRequestUpdatesForMiniServiceRequest(ctx, req, req.Status.Instances[instanceID])
	require.NoError(t, err)
	assert.Empty(t, up)

	ms := &v1alpha1.MiniService{}
	ms.Name = instanceID
	err = clients.HelmV2.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	require.Equal(t, &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "sr-reqID-miniservice",
			ResourceVersion: "1",
			Labels: map[string]string{
				nvcatypes.TaskIDUpperKey:          "taskid",
				nvcatypes.TaskIDKey:               "taskid",
				"gpu-name":                        "",
				nvcatypes.ICMSRequestIDKey:        "reqID",
				nvcatypes.NCAIDKey:                "nca-ncaID-nca",
				nvcatypes.NCAIDUpperKey:           "nca-ncaID-nca",
				"nvcf.nvidia.io/message-batch-id": "",
			},
			Annotations: map[string]string{
				"instance-count":           "1",
				nvcatypes.ICMSRequestIDKey: "reqID",
				nvcatypes.NCAIDKey:         "ncaID",
				nvcatypes.ClusterGroupKey:  "",
			},
		},
		Spec: v1alpha1.MiniServiceSpec{
			Namespace:       req.Name,
			ICMSRequestName: req.Name,
			HelmChartConfig: common.HelmConfig{
				URL:    "https://helm.ngc.nvidia.com/qtfpt1h0bieu/byoc/charts/testchart-0.1.0.tgz",
				Values: createMsg.LaunchSpecification.Values,
			},
		},
	}, ms)

	ms.Status.Phase = v1alpha1.MiniServiceInstalling
	err = clients.HelmV2.Status().Update(ctx, ms)
	require.NoError(t, err)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		bc.ForceSync(ctx)
		req, err := srClient.Get(ctx, srName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		if assert.Len(ct, req.Status.Instances, 1) {
			assert.Equal(ct, nvcav2beta1.InstanceStatus{
				ID:     instanceID,
				Type:   nvcav2beta1.InstanceTypeMiniService,
				Status: string(nvcatypes.ICMSInstanceStarted),
			}, req.Status.Instances[instanceID])
		}
		assert.Equal(ct, nvcav2beta1.ICMSRequestStatusInProgress, req.Status.RequestStatus)
	}, 5*time.Second, 100*time.Millisecond)

	ms.Status.Phase = v1alpha1.MiniServiceStarting
	err = clients.HelmV2.Status().Update(ctx, ms)
	require.NoError(t, err)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		bc.ForceSync(ctx)
		req, err := srClient.Get(ctx, srName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		if assert.Len(ct, req.Status.Instances, 1) {
			assert.Equal(ct, nvcav2beta1.InstanceStatus{
				ID:     instanceID,
				Type:   nvcav2beta1.InstanceTypeMiniService,
				Status: string(nvcatypes.ICMSInstanceStarted),
			}, req.Status.Instances[instanceID])
		}
		assert.Equal(ct, nvcav2beta1.ICMSRequestStatusInstancesInProgress, req.Status.RequestStatus)
	}, 5*time.Second, 100*time.Millisecond)

	ms.Status.Phase = v1alpha1.MiniServiceFailed
	ms.Status.Conditions = []metav1.Condition{
		{
			Reason:  v1alpha1.MiniServiceStatusReasonObjectsFailed,
			Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
			Status:  metav1.ConditionFalse,
			Message: "v1.Pod pod:\t\nsome issue",
		},
	}
	err = clients.HelmV2.Status().Update(ctx, ms)
	require.NoError(t, err)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		bc.ForceSync(ctx)
		req, err := srClient.Get(ctx, srName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		if assert.Len(ct, req.Status.Instances, 1) {
			assert.Equal(ct, nvcav2beta1.InstanceStatus{
				ID:     instanceID,
				Type:   nvcav2beta1.InstanceTypeMiniService,
				Status: string(nvcatypes.ICMSInstanceStarted),
			}, req.Status.Instances[instanceID])
		}
		assert.Equal(ct, nvcav2beta1.ICMSRequestStatusFailed, req.Status.RequestStatus)
	}, 5*time.Second, 100*time.Millisecond)

	req, err = srClient.Get(ctx, srName, metav1.GetOptions{})
	require.NoError(t, err)
	up, err = bc.k8sArtifactHelper.(K8sComputeBackend).GetICMSRequestUpdatesForMiniServiceRequest(ctx, req, req.Status.Instances[instanceID])
	require.NoError(t, err)
	assert.NotEmpty(t, up)
	assert.Equal(t, up.InstanceID, instanceID)
	assert.Equal(t, nvcatypes.ICMSInstanceRequestClosed, up.Payload.RequestState)
}

func TestCreateMiniService_Task(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })

	ctx, cancel := context.WithCancel(newTestContext())
	reg := prometheus.NewRegistry()
	ctx = nvcametrics.WithDefaultMetrics(ctx,
		"my-nca-id", "my-cluster", "my-cluster", "1.0.0",
		nvcametrics.WithEventErrorTotalDefaultEvents(append(getAgentEvents(), getNVCAMetricEvents()...)),
		nvcametrics.WithContainerCrashAndRestartTotalDefaultContainerNames(GetDefaultWorkloadContainerNamesToWatch()),
		nvcametrics.WithRegisterer(reg))
	t.Cleanup(cancel)

	inferenceTestataDir := filepath.Join("testdata", "rendered-inference-test")
	ncaID := "ncaID"
	reqID := "reqID"
	msgID := "msgID"
	miniServiceNamespace := "sr-" + reqID

	objs, _ := decodeInferenceManifests(t, inferenceTestataDir, miniServiceNamespace)

	// Create the objects that would be created by the MiniService controller.
	clients := mockKubeClients(objs...)

	bc, _, err := NewBackendk8sCacheBuilder().
		WithClients(clients).
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithStaticGPUCapacity(10).
		Start(ctx)
	require.NoError(t, err)

	srClient := clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace)

	createMsg := task.CreationQueueMessage{
		Details: task.Details{
			TaskID:   "taskid",
			TaskType: "HELMCHART",
		},
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			RequestID:     reqID,
			NCAID:         ncaID,
			Action:        common.TaskCreationAction,
			InstanceType:  "DGXCLOUD.GPU.A100",
			InstanceCount: 1,
		},
		LaunchSpecification: task.LaunchSpecification{
			CloudProvider:                  "DGXCLOUD",
			ICMSEnvironment:                "prod",
			ResultHandlingStrategy:         "UPLOAD",
			TerminationGracePeriodDuration: "PT10M",
			MaxRuntimeDuration:             "PT2H",
			MaxQueuedDuration:              "PT2M",
			EnvironmentB64:                 "RVNTX0FHRU5UX0NPTlRBSU5FUj1zdGcubnZjci5pby9udi1jZi9udmNmLWNvcmUvZXNzLWFnZW50OjEuMC4wCklOSVRfQ09OVEFJTkVSPW52Y3IuaW8vcXRmcHQxaDBiaWV1L252Y2YtY29yZS9udmNmX3dvcmtlcl9pbml0OjAuMjQuMTAKTUFYX1JFUVVFU1RfQ09OQ1VSUkVOQ1k9IjEiCk5DQV9JRD1fbElMWEItMU5mTm1CblFTa19zcHFWV090Q0FYUW01MFVFTXdqM1RSZ3ltSkoyQXl1d2NneHEKTlZDRl9GUUROPWh0dHBzOi8vdXMtd2VzdC0yLmFwaS5udmNmLm52aWRpYS5jb20KTlZDRl9GUUROX0dSUEM9aHR0cHM6Ly9ncnBjLmFwaS5udmNmLm52aWRpYS5jb20KTlZDRl9GUUROX05BVFM9dGxzOi8vdXMtd2VzdC0yLmF3cy5jbG91ZC5uYXRzLm52Y2YubnZpZGlhLmNvbTo0MjIyCk5WQ0ZfV09SS0VSX1RPS0VOPXRvawpPVEVMX0NPTlRBSU5FUj1udmNyLmlvL3F0ZnB0MWgwYmlldS9udmNmLWNvcmUvb3BlbnRlbGVtZXRyeS1jb2xsZWN0b3I6MC43NC4wCk9URUxfRVhQT1JURVJfT1RMUF9FTkRQT0lOVD1odHRwczovL3Byb2Qub3RlbC5rYWl6ZW4ubnZpZGlhLmNvbTo4MjgyClNFQ1JFVFNfQVNTRVJUSU9OX1RPS0VOPWV5SmhiR2NpT2lKU1V6STFOaUlzSW5SNWNDSTZJa3BYVkNKOS5leUp6ZFdJaU9pSXhNak0wTlRZM09Ea3dJaXdpYm1GdFpTSTZJa3B2YUc0Z1JHOWxJaXdpWVhOelpYSjBhVzl1SWpwN0luTmxZM0psZEZCaGRHaHpJanBiSW1GalkyOTFiblJ6TDE5c1NVeFlRaTB4VG1aT2JVSnVVVk5yWDNOd2NWWlhUM1JEUVZoUmJUVXdWVVZOZDJvelZGSm5lVzFLU2pKQmVYVjNZMmQ0Y1M5MFpXeGxiV1YwY25rdk5tWmhPRE0yTm1VdE4ySmhNaTAwTVRRekxXRTNORFF0TnpVeE5XWmhaamxpWmpjeUlpd2lZV05qYjNWdWRITXZYMnhKVEZoQ0xURk9aazV0UW01UlUydGZjM0J4VmxkUGRFTkJXRkZ0TlRCVlJVMTNhak5VVW1kNWJVcEtNa0Y1ZFhkalozaHhMM1JsYkdWdFpYUnllUzgyWm1FNE16WTJaUzAzWW1FeUxUUXhORE10WVRjME5DMDNOVEUxWm1GbU9XSm1PREVpWFgwc0ltRmtiV2x1SWpwMGNuVmxMQ0pwWVhRaU9qRTFNVFl5TXprd01qSjkuU3BRWWJGZTFuZnJRNUtzaFJseTlTVUMyNldfajJwUWg2RE1pbnNicnNRSHZLZzFzZTJvSDNWem9pbmJNYlF6XzVMWGNnLVhOa3g0Y05KTjJBanV3VUl6azZESVVMSUNIZXF1anEteEFhZ0ZSOF96MjVvMTFkMDF6QlM1TnhGOUFDZ3RJbDY5ZGhUSGs4c0syZVFiNEFGR0NGZmY2MWowa1hhYklZRVNHSnhkdjlSa05mV3RZWi1GbUljOXVGNGpZNTl6UjFFQmRYaWxjY2NSMFJpQ3ZLQWxUWW9yRTdUai0wNEtnVEZudlFibVAwVFFHUWQ2eGJxZEFhUFJCcFh5QkcwNHFtQTI5NlRmckFPVl8wMmFJZHRqSGE1U2ptdnRQQWJWZUhWVjVCeF9aZDh5Vm15TjBlN3FlZ25BT3FjNU5QM2tGMzhXNG5XaFRFOFZrTlRaalRQQQpTSURFQ0FSX0NSRURFTlRJQUw9Y29udGFpbmVyLWNyZWQtMQpUQVNLX0NPTlRBSU5FUj1udmNyLmlvL215b3JnL2dwdC0zLjUtdHVyYm8tZmluZS10dW5lOjEuMC4wClRBU0tfQ09OVEFJTkVSX0FSR1M9LWFyZzE9dGVzdDEgYXJnMj10ZXN0MgpUQVNLX0NPTlRBSU5FUl9DUkVERU5USUFMPWNvbnRhaW5lci1jcmVkLTEKVEFTS19DT05UQUlORVJfRU5WPVczc2lhMlY1SWpvaVZFRlRTMTlGVGxaZlMwVlpJaXdpZG1Gc2RXVWlPaUowWVhOclgzWmhiSFZsSW4xZApUQVNLX0hFQUxUSF9FTkRQT0lOVD0vdjIvaGVhbHRoL3JlYWR5ClRBU0tfSEVBTFRIX0VYUEVDVEVEX1JFU1BPTlNFX0NPREU9IjIwMCIKVEFTS19IRUFMVEhfUE9SVD0iNTAwNTEiClRBU0tfSUQ9MTNlMmI1OTktOTZjYS00MmI1LWE0MTktOGZhN2Y3MDFkNWQyClRBU0tfTkFNRT1teS10YXNrClRBU0tfUE9SVD0iNTAwNTEiClRBU0tfUFJPVE9DT0w9R1JQQwpUQVNLX1NFQ1JFVFNfUFJFU0VOVD10cnVlClRBU0tfVVJMPS9ncnBjClRFUk1JTkFUSU9OX0dSQUNFX1BFUklPRD1QVDJIClRSQUNJTkdfQUNDRVNTX1RPS0VOPXRyYWNlLXRvay0xClVUSUxTX0NPTlRBSU5FUj1udmNyLmlvL3F0ZnB0MWgwYmlldS9udmNmLWNvcmUvbnZjZl93b3JrZXJfdXRpbHM6Mi4yMS40Cg==",
			HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
				HelmChartURL: "https://helm.ngc.nvidia.com/qtfpt1h0bieu/byoc/charts/testchart-0.1.0.tgz",
				Values:       []byte(`{"foo":{"bar":"baz"}}`),
			},
		},
	}
	sr, err := bc.CreateICMSCreationMessageRequest(ctx, createMsg, "receipt1", msgID, "")
	require.NoError(t, err)
	assert.NotNil(t, sr)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	req, err := srClient.Get(ctx, "sr-"+reqID, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, req.Status.Instances, 0)

	// Mimic Agent.PutICMSRequestAcknowledgement()
	applied := bc.applyICMSRequestStatusChange(ctx, req.DeepCopy(), func(_ context.Context, s *nvcav2beta1.ICMSRequest) {
		s.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusPending
	})
	require.True(t, applied)

	srName := req.Name
	instanceID := req.Name + "-miniservice"

	// Force sync to ensure the status update is propagated to the lister
	bc.ForceSync(ctx)

	// Wait for both API and lister to see the status update before syncing (may already be InProgress if workers are fast)
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		// Check API directly
		reqAPI, err := srClient.Get(ctx, srName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		if !assert.NotEmpty(ct, reqAPI.Status.RequestStatus, "status should be set in API") {
			return
		}
		// Check lister
		sr, err := bc.icmsRequestLister.ICMSRequests(bc.requestsNamespace).Get(srName)
		if assert.NoError(ct, err) {
			assert.NotEmpty(ct, sr.Status.RequestStatus, "status should be set in lister")
		}
	}, 5*time.Second, 50*time.Millisecond)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		err = bc.SyncAllICMSRequests(ctx)
		assert.NoError(ct, err)
		bc.ForceSync(ctx)
		req, err := srClient.Get(ctx, srName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		assert.Len(ct, req.Status.Instances, 1)
		assert.Contains(ct, req.Status.Instances, instanceID)
		assert.Equal(ct, nvcav2beta1.ICMSRequestStatusInProgress, req.Status.RequestStatus)
	}, 15*time.Second, 100*time.Millisecond)

	req, err = srClient.Get(ctx, srName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Contains(t, req.Status.Instances, instanceID)
	statusForInstanceID := req.Status.Instances[instanceID]
	assert.Equal(t, nvcav2beta1.InstanceStatus{
		ID:                    instanceID,
		Type:                  nvcav2beta1.InstanceTypeMiniService,
		Status:                string(nvcatypes.ICMSInstanceStarted),
		LastReportedStatus:    string(nvcatypes.ICMSInstanceStateNoStatus),
		LastReportedTimestamp: nil,
	}, statusForInstanceID)

	// No phase on miniservice yet, should be an empty status update.
	up, err := bc.k8sArtifactHelper.(K8sComputeBackend).GetICMSRequestUpdatesForMiniServiceRequest(ctx, req, req.Status.Instances[instanceID])
	require.NoError(t, err)
	assert.Empty(t, up)

	ms := &v1alpha1.MiniService{}
	ms.Name = instanceID
	err = clients.HelmV2.Get(ctx, client.ObjectKeyFromObject(ms), ms)
	require.NoError(t, err)
	require.Equal(t, &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "sr-reqID-miniservice",
			ResourceVersion: "1",
			Labels: map[string]string{
				nvcatypes.TaskIDUpperKey:          "taskid",
				nvcatypes.TaskIDKey:               "taskid",
				"gpu-name":                        "",
				nvcatypes.ICMSRequestIDKey:        "reqID",
				nvcatypes.NCAIDKey:                "nca-ncaID-nca",
				nvcatypes.NCAIDUpperKey:           "nca-ncaID-nca",
				"nvcf.nvidia.io/message-batch-id": "",
			},
			Annotations: map[string]string{
				"instance-count":           "1",
				nvcatypes.ICMSRequestIDKey: "reqID",
				nvcatypes.NCAIDKey:         "ncaID",
				nvcatypes.ClusterGroupKey:  "",
			},
		},
		Spec: v1alpha1.MiniServiceSpec{
			Namespace:       req.Name,
			ICMSRequestName: req.Name,
			HelmChartConfig: common.HelmConfig{
				URL:    "https://helm.ngc.nvidia.com/qtfpt1h0bieu/byoc/charts/testchart-0.1.0.tgz",
				Values: createMsg.LaunchSpecification.Values,
			},
		},
	}, ms)

	ms.Status.Phase = v1alpha1.MiniServiceInstalling
	err = clients.HelmV2.Status().Update(ctx, ms)
	require.NoError(t, err)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		bc.ForceSync(ctx)
		req, err := srClient.Get(ctx, srName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		if assert.Len(ct, req.Status.Instances, 1) {
			assert.Equal(ct, nvcav2beta1.InstanceStatus{
				ID:     instanceID,
				Type:   nvcav2beta1.InstanceTypeMiniService,
				Status: string(nvcatypes.ICMSInstanceStarted),
			}, req.Status.Instances[instanceID])
		}
		assert.Equal(ct, nvcav2beta1.ICMSRequestStatusInstancesInProgress, req.Status.RequestStatus)
	}, 5*time.Second, 100*time.Millisecond)

	ms.Status.Phase = v1alpha1.MiniServiceStarting
	err = clients.HelmV2.Status().Update(ctx, ms)
	require.NoError(t, err)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		bc.ForceSync(ctx)
		req, err := srClient.Get(ctx, srName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		if assert.Len(ct, req.Status.Instances, 1) {
			assert.Equal(ct, nvcav2beta1.InstanceStatus{
				ID:     instanceID,
				Type:   nvcav2beta1.InstanceTypeMiniService,
				Status: string(nvcatypes.ICMSInstanceStarted),
			}, req.Status.Instances[instanceID])
		}
		assert.Equal(ct, nvcav2beta1.ICMSRequestStatusInstancesInProgress, req.Status.RequestStatus)
	}, 5*time.Second, 100*time.Millisecond)

	ms.Status.Phase = v1alpha1.MiniServiceFailed
	ms.Status.Conditions = []metav1.Condition{
		{
			Reason:  v1alpha1.MiniServiceStatusReasonObjectsFailed,
			Type:    v1alpha1.MiniServiceConditionObjectsHealthy,
			Status:  metav1.ConditionFalse,
			Message: "v1.Pod pod:\t\nsome issue",
		},
	}
	err = clients.HelmV2.Status().Update(ctx, ms)
	require.NoError(t, err)

	bc.ForceSync(ctx)
	err = bc.SyncAllICMSRequests(ctx)
	require.NoError(t, err)
	bc.ForceSync(ctx)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		bc.ForceSync(ctx)
		req, err := srClient.Get(ctx, srName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		if assert.Len(ct, req.Status.Instances, 1) {
			assert.Equal(ct, nvcav2beta1.InstanceStatus{
				ID:     instanceID,
				Type:   nvcav2beta1.InstanceTypeMiniService,
				Status: string(nvcatypes.ICMSInstanceStarted),
			}, req.Status.Instances[instanceID])
		}
		assert.Equal(ct, nvcav2beta1.ICMSRequestStatusFailed, req.Status.RequestStatus)
	}, 5*time.Second, 100*time.Millisecond)

	req, err = srClient.Get(ctx, srName, metav1.GetOptions{})
	require.NoError(t, err)
	up, err = bc.k8sArtifactHelper.(K8sComputeBackend).GetICMSRequestUpdatesForMiniServiceRequest(ctx, req, req.Status.Instances[instanceID])
	require.NoError(t, err)
	assert.NotEmpty(t, up)
	assert.Equal(t, up.InstanceID, instanceID)
	assert.Equal(t, nvcatypes.ICMSInstanceRequestClosed, up.Payload.RequestState)
}

// Create the objects that would be created by the MiniService controller.
func decodeInferenceManifests(t *testing.T, testdataDir, miniServiceNamespace string) (objs []runtime.Object, miniServicePodName string) {
	unstObjs := decodeYAMLFile(t, filepath.Join(testdataDir, "manifests.yaml"))
	for _, unstObj := range unstObjs {
		unstObj.SetNamespace(miniServiceNamespace)

		objJSON, err := unstObj.MarshalJSON()
		require.NoError(t, err)

		var obj runtime.Object
		switch gvk := unstObj.GroupVersionKind(); gvk {
		case appsv1.SchemeGroupVersion.WithKind("Deployment"):
			dep := &appsv1.Deployment{}
			obj = dep

			err = json.Unmarshal(objJSON, dep)
			require.NoError(t, err)
			pod := &v1.Pod{
				ObjectMeta: dep.Spec.Template.ObjectMeta,
				Spec:       dep.Spec.Template.Spec,
			}
			pod.Name = dep.Name + "-pod"
			miniServicePodName = pod.Name
			pod.Namespace = miniServiceNamespace
			objs = append(objs, pod)
		case v1.SchemeGroupVersion.WithKind("Secret"):
			obj = &v1.Secret{}
		case v1.SchemeGroupVersion.WithKind("Service"):
			obj = &v1.Service{}
		default:
			require.Fail(t, "Need to add GVK to test:", gvk)
		}

		err = json.Unmarshal(objJSON, obj)
		require.NoError(t, err)
		objs = append(objs, obj)
	}

	return objs, miniServicePodName
}

func decodeYAMLFile(t *testing.T, fp string) []*unstructured.Unstructured {
	t.Helper()

	f, err := os.Open(fp)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })

	yr := utilyaml.NewYAMLReader(bufio.NewReader(f))

	var objs []*unstructured.Unstructured
	for {
		doc, err := yr.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
		}

		obj := &unstructured.Unstructured{
			Object: map[string]interface{}{},
		}

		err = sigsyaml.Unmarshal(doc, &obj.Object)
		require.NoError(t, err)

		objs = append(objs, obj)
	}

	return objs
}

// TestEnsureHelmChartRBACFromConfigMap_PreservesRules proves the YAML decoder
// honors the Kubernetes API JSON tags (apiGroups, resources, verbs) when
// populating rbacv1.Role. With gopkg.in/yaml.v2 these fields end up empty
// because the struct only declares JSON tags, so the apiserver rejects the
// Role with "resource rules must supply at least one api group".
func TestEnsureHelmChartRBACFromConfigMap_PreservesRules(t *testing.T) {
	ctx := newTestContext()

	clients := mockKubeClients()
	bc := &BackendK8sCache{
		clients:         clients,
		systemNamespace: SystemNamespace,
	}

	rbacCM := newHelmInstanceRBACConfigMap()
	namespace := "test-ns"

	_, err := bc.ensureHelmChartRBACFromConfigMap(ctx, namespace, rbacCM)
	require.NoError(t, err)

	role, err := clients.K8s.RbacV1().Roles(namespace).
		Get(ctx, helmChartInstanceRoleName, metav1.GetOptions{})
	require.NoError(t, err)

	require.NotEmpty(t, role.Rules, "Role.Rules must be populated; empty rules would be rejected by the apiserver")
	assert.Equal(t, []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"pods", "services", "secrets"}, Verbs: []string{"*"}},
		{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"*"}},
		{APIGroups: []string{"rbac.authorization.k8s.io"}, Resources: []string{"roles", "rolebindings"}, Verbs: []string{"*"}},
	}, role.Rules)
}
