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
	"context"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcainformers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/informers/externalversions"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
	mockqueue "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// TestCreationRejectedWhenBYOODisabled covers the function and task paths: a
// creation message that requests telemetries while the required feature flags
// are off is persisted as rejected, fails sync with a terminal error, and is
// reported to ICMS as terminated with an actionable reason.
func TestCreationRejectedWhenBYOODisabled(t *testing.T) {
	tests := []struct {
		name         string
		msg          func(t *testing.T) translate.CreationQueueMessageMetadataGetter
		enabled      []*featureflag.FeatureFlag
		wantWorkload string
		wantFlags    string
	}{
		{
			name: "container function with all flags disabled",
			msg: func(t *testing.T) translate.CreationQueueMessageMetadataGetter {
				return decodeCM(t, creationgMsgFunctionContainerLaunchSpecBYOO)
			},
			wantWorkload: "the function requests telemetries",
			wantFlags:    "feature flags BYOObservability, UseFunctionTranslator are not enabled",
		},
		{
			name: "container function without function translator",
			msg: func(t *testing.T) translate.CreationQueueMessageMetadataGetter {
				return decodeCM(t, creationgMsgFunctionContainerLaunchSpecBYOO)
			},
			enabled:      []*featureflag.FeatureFlag{featureflag.BYOObservability},
			wantWorkload: "the function requests telemetries",
			wantFlags:    "feature flags UseFunctionTranslator are not enabled",
		},
		{
			name: "helm function with all flags disabled",
			msg: func(t *testing.T) translate.CreationQueueMessageMetadataGetter {
				return decodeCM(t, creationgMsgFunctionHelmLaunchSpecBYOO)
			},
			wantWorkload: "the function requests telemetries",
			wantFlags:    "feature flags BYOObservability, UseFunctionTranslator are not enabled",
		},
		{
			name: "task with BYOO disabled",
			msg: func(t *testing.T) translate.CreationQueueMessageMetadataGetter {
				return decodeCMTask(t, cmsgTaskContainerBYOO)
			},
			enabled:      []*featureflag.FeatureFlag{featureflag.UseFunctionTranslator},
			wantWorkload: "the task requests telemetries",
			wantFlags:    "feature flags BYOObservability are not enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(newTestContext())
			t.Cleanup(cancel)

			clients := mockKubeClients()
			fff := &featureflagmock.Fetcher{EnabledFFs: tt.enabled}
			bc, _, err := NewBackendk8sCacheBuilder().
				WithNamespaceLabels(labels.Set{"foo": "bar"}).
				WithClients(clients).
				WithFeatureFlagFetcher(fff).
				Start(ctx)
			require.NoError(t, err)

			sr, err := bc.CreateICMSCreationMessageRequest(ctx, tt.msg(t), "mrecpt", "msgid", "")
			require.NoError(t, err)
			require.NotNil(t, sr)

			reason := GetCreationRejectionReason(sr)
			assert.Contains(t, reason, "Bring Your Own Observability (BYOO) is disabled on this cluster")
			assert.Contains(t, reason, tt.wantWorkload)
			assert.Contains(t, reason, tt.wantFlags)

			// The rejected request is persisted so the failure can be reported.
			persisted, err := clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).
				Get(ctx, sr.Name, metav1.GetOptions{})
			require.NoError(t, err)
			assert.Equal(t, reason, GetCreationRejectionReason(persisted))

			// Sync never applies a rejected request, it fails it as terminal.
			persisted.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusPending
			err = bc.syncICMSRequest(ctx, persisted)
			require.Error(t, err)
			assert.True(t, nvcaerrors.IsTerminal(err))
			assert.Contains(t, err.Error(), reason)

			// Each placeholder instance is reported as terminated with the reason.
			instanceID := getPodName("0-" + persisted.Name)
			persisted.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusFailed
			persisted.Status.Instances = map[string]nvcav2beta1.InstanceStatus{
				instanceID: {
					ID:                 instanceID,
					Type:               nvcav2beta1.InstanceTypePod,
					Status:             string(types.ICMSInstanceStarted),
					LastReportedStatus: string(types.ICMSInstanceStateNoStatus),
				},
			}
			kb := K8sComputeBackend{clients: clients, bk8s: bc}
			updates := kb.GetICMSRequestUpdatesForCreateRequest(ctx, persisted)
			require.Len(t, updates, 1)
			assert.Equal(t, persisted.Spec.RequestID, updates[0].RequestID)
			assert.Equal(t, instanceID, updates[0].InstanceID)
			payload := updates[0].Payload
			assert.Equal(t, types.ICMSInstanceTerminated, payload.InstanceState)
			assert.Equal(t, types.ICMSInstanceRequestClosed, payload.RequestState)
			assert.Equal(t, types.ICMSRequestInstanceTerminatedByService, payload.Status)
			assert.Equal(t, types.ICMSInstanceTerminatedTerminalError, payload.TerminationCause)
			assert.Equal(t, reason, payload.HealthInfo.ErrorLog)

			// Once reported, the instance is not reported again.
			persisted.Status.Instances[instanceID] = nvcav2beta1.InstanceStatus{
				ID:                    instanceID,
				Type:                  nvcav2beta1.InstanceTypePod,
				Status:                string(types.ICMSInstanceTerminated),
				LastReportedStatus:    string(types.ICMSInstanceTerminated),
				LastReportedTimestamp: &metav1.Time{Time: persisted.Status.LastStatusUpdated.Time},
			}
			assert.Empty(t, kb.GetICMSRequestUpdatesForCreateRequest(ctx, persisted))
		})
	}
}

func TestCreationAcceptedWhenBYOOEnabled(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	clients := mockKubeClients()
	fff := &featureflagmock.Fetcher{EnabledFFs: []*featureflag.FeatureFlag{
		featureflag.BYOObservability,
		featureflag.UseFunctionTranslator,
	}}
	bc, _, err := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithClients(clients).
		WithFeatureFlagFetcher(fff).
		Start(ctx)
	require.NoError(t, err)

	sr, err := bc.CreateICMSCreationMessageRequest(ctx,
		decodeCM(t, creationgMsgFunctionContainerLaunchSpecBYOO), "mrecpt1", "msgid1", "")
	require.NoError(t, err)
	assert.Empty(t, GetCreationRejectionReason(sr))

	sr, err = bc.CreateICMSCreationMessageRequest(ctx,
		decodeCMTask(t, cmsgTaskContainerBYOO), "mrecpt2", "msgid2", "")
	require.NoError(t, err)
	assert.Empty(t, GetCreationRejectionReason(sr))
}

func TestCreationRejectedWhenEnvOverridesFail(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	clients := mockKubeClients()
	bc, _, err := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"foo": "bar"}).
		WithClients(clients).
		WithFeatureFlagFetcher(&featureflagmock.Fetcher{}).
		Start(ctx)
	require.NoError(t, err)
	bc.taskEnvOverrides = map[string]string{"FOO": "bar"}

	msg := decodeCMTask(t, cmsgTaskContainer)
	msg.LaunchSpecification.EnvironmentB64 = "not-base64!"

	sr, err := bc.CreateICMSCreationMessageRequest(ctx, msg, "mrecpt", "msgid", "")
	require.NoError(t, err)
	assert.Contains(t, GetCreationRejectionReason(sr), "apply task env overrides")
}

// TestDoCreationMessageDeletesRejectedMessage checks that a rejected creation
// message is removed from the queue instead of being made visible again, so it
// cannot start a workload later.
func TestDoCreationMessageDeletesRejectedMessage(t *testing.T) {
	ctx := newTestContext()

	clients := mockKubeClients()
	bk8s := &BackendK8sCache{
		featureFlagFetcher: &featureflagmock.Fetcher{},
		clients:            clients,
		requestsNamespace:  RequestsNamespace,
	}
	bk8s.icmsRequestLister = mustICMSRequestLister(t, nvcainformers.NewSharedInformerFactoryWithOptions(
		clients.BART,
		ResyncInterval,
		nvcainformers.WithNamespace(bk8s.requestsNamespace)))

	gpuName := types.GPUName("A100")
	qi := queue.MessageQueueInfo{QueueURL: "urla100c"}
	qc := &mockqueue.Client{}
	qm := NewQueueManager(
		bk8s,
		staticStatusGetter{status: types.AgentHealth{
			GPUUsage: types.GPUResourceSet{gpuName: {Capacity: 8}},
		}},
		qc,
		types.QueueCredentials{
			CreationQueues:   map[types.GPUName]queue.MessageQueueInfo{gpuName: qi},
			TerminationQueue: queue.MessageQueueInfo{QueueURL: "termurl"},
		},
		featureflag.DefaultFetcher,
		types.MaintenanceModeNone,
		nvcametrics.FromContext(ctx),
	)

	msg := queue.ReceiveMessageOutput{
		MessageID:     "msgid-byoo",
		ReceiptHandle: "rhdl-byoo",
		Body:          cmsgTaskContainerBYOO,
	}
	qc.AddMessage(qi.QueueURL, msg)

	consumed, err := qm.doCreationMessage(ctx, gpuName, 0, qi, msg)
	require.NoError(t, err)
	assert.Zero(t, consumed)

	// The message is gone from the queue.
	err = qc.DeleteMessage(ctx, queue.DeleteMessageInput{QueueInfo: qi, ReceiptHandle: msg.ReceiptHandle})
	assert.True(t, qc.IsMessageNotFoundError(err))

	reqs, err := clients.BART.NvcaV2beta1().ICMSRequests(bk8s.requestsNamespace).
		List(ctx, metav1.ListOptions{LabelSelector: labels.Set{SQSMessageIDKey: msg.MessageID}.String()})
	require.NoError(t, err)
	require.Len(t, reqs.Items, 1)
	assert.Contains(t, GetCreationRejectionReason(&reqs.Items[0]), "BYOObservability")
}

type staticStatusGetter struct {
	status types.AgentHealth
}

func (s staticStatusGetter) GetStatus() types.AgentHealth { return s.status }

func (s staticStatusGetter) RefreshStatus(context.Context) (types.AgentHealth, error) {
	return s.status, nil
}
