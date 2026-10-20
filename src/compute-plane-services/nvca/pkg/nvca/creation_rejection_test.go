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
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	fakebartclient "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"
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
	const (
		functionReason = "Bring Your Own Observability (BYOO) is disabled on this cluster: " +
			"the function requests telemetries, but these NVCA feature flags are not enabled: " +
			"BYOObservability, UseFunctionTranslator. " +
			"Enable them on the cluster, or deploy without telemetries."
		taskReason = "Bring Your Own Observability (BYOO) is disabled on this cluster: " +
			"the task requests telemetries, but these NVCA feature flags are not enabled: " +
			"BYOObservability. " +
			"Enable them on the cluster, or deploy without telemetries."
	)
	tests := []struct {
		name    string
		msg     func(t *testing.T) translate.CreationQueueMessageMetadataGetter
		enabled []*featureflag.FeatureFlag
		want    string
	}{
		{
			name: "container function with all flags disabled",
			msg: func(t *testing.T) translate.CreationQueueMessageMetadataGetter {
				return decodeCM(t, creationgMsgFunctionContainerLaunchSpecBYOO)
			},
			want: functionReason,
		},
		{
			name: "container function without function translator",
			msg: func(t *testing.T) translate.CreationQueueMessageMetadataGetter {
				return decodeCM(t, creationgMsgFunctionContainerLaunchSpecBYOO)
			},
			enabled: []*featureflag.FeatureFlag{featureflag.BYOObservability},
			want: "Bring Your Own Observability (BYOO) is disabled on this cluster: " +
				"the function requests telemetries, but these NVCA feature flags are not enabled: " +
				"UseFunctionTranslator. " +
				"Enable them on the cluster, or deploy without telemetries.",
		},
		{
			name: "helm function with all flags disabled",
			msg: func(t *testing.T) translate.CreationQueueMessageMetadataGetter {
				return decodeCM(t, creationgMsgFunctionHelmLaunchSpecBYOO)
			},
			want: functionReason,
		},
		{
			name: "task with BYOO disabled",
			msg: func(t *testing.T) translate.CreationQueueMessageMetadataGetter {
				return decodeCMTask(t, cmsgTaskContainerBYOO)
			},
			enabled: []*featureflag.FeatureFlag{featureflag.UseFunctionTranslator},
			want:    taskReason,
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
			assert.Equal(t, tt.want, reason)

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

			// Turning the flags on restarts NVCA with new flags. The restarted
			// agent still fails the rejected request instead of applying it,
			// and does not apply a redelivered copy of the message.
			restarted, _, err := NewBackendk8sCacheBuilder().
				WithNamespaceLabels(labels.Set{"foo": "bar"}).
				WithClients(clients).
				WithFeatureFlagFetcher(&featureflagmock.Fetcher{EnabledFFs: []*featureflag.FeatureFlag{
					featureflag.BYOObservability,
					featureflag.UseFunctionTranslator,
				}}).
				Start(ctx)
			require.NoError(t, err)
			err = restarted.syncICMSRequest(ctx, persisted)
			require.Error(t, err)
			assert.True(t, nvcaerrors.IsTerminal(err))
			redelivered, err := restarted.CreateICMSCreationMessageRequest(ctx, tt.msg(t), "mrecpt2", "msgid", "")
			require.NoError(t, err)
			assert.Nil(t, redelivered)

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
	assert.Contains(t, GetCreationRejectionReason(sr), "NVCA could not apply the cluster's task environment overrides")
}

// TestDoCreationMessageDeletesRejectedMessage checks that a transient failure
// keeps the creation message for a retry, while a rejected creation message is
// removed from the queue instead of being made visible again, so it cannot
// start a workload later.
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

	listRequests := func() []nvcav2beta1.ICMSRequest {
		t.Helper()
		reqs, err := clients.BART.NvcaV2beta1().ICMSRequests(bk8s.requestsNamespace).
			List(ctx, metav1.ListOptions{LabelSelector: labels.Set{SQSMessageIDKey: msg.MessageID}.String()})
		require.NoError(t, err)
		return reqs.Items
	}

	// A transient API failure is not a rejection: the message stays for a retry.
	failCreate := true
	clients.BART.(*fakebartclient.Clientset).PrependReactor("create", "icmsrequests",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			if failCreate {
				failCreate = false
				return true, nil, errors.New("apiserver unavailable")
			}
			return false, nil, nil
		})
	_, err := qm.doCreationMessage(ctx, gpuName, 0, qi, msg)
	require.Error(t, err)
	assert.Empty(t, listRequests())
	pending, err := qc.PeekMessage(ctx, queue.ReceiveMessageInput{QueueInfo: qi, MaxNumberOfMessages: 1})
	require.NoError(t, err)
	assert.Len(t, pending, 1)

	consumed, err := qm.doCreationMessage(ctx, gpuName, 0, qi, msg)
	require.NoError(t, err)
	assert.Zero(t, consumed)

	// The message is gone from the queue.
	err = qc.DeleteMessage(ctx, queue.DeleteMessageInput{QueueInfo: qi, ReceiptHandle: msg.ReceiptHandle})
	assert.True(t, qc.IsMessageNotFoundError(err))

	reqs := listRequests()
	require.Len(t, reqs, 1)
	assert.Contains(t, GetCreationRejectionReason(&reqs[0]), "BYOObservability")
}

// TestSyncQueuesDeletesRedeliveredRejectedMessage checks that a rejected
// creation message delivered again with a new receipt handle is deleted with
// that handle, while a redelivered message for an accepted request is only
// skipped.
func TestSyncQueuesDeletesRedeliveredRejectedMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	clients := mockKubeClients()
	bk8s := &BackendK8sCache{
		featureFlagFetcher: &featureflagmock.Fetcher{},
		clients:            clients,
		requestsNamespace:  RequestsNamespace,
	}

	gpuName := types.GPUName("A100")
	rejectedQueue := queue.MessageQueueInfo{GPU: string(gpuName), QueueURL: "rejected_url", QueueType: queue.CreationQueue}
	acceptedQueue := queue.MessageQueueInfo{GPU: string(gpuName), QueueURL: "accepted_url", QueueType: queue.CreationQueue}

	// Both requests already exist from the first delivery of each message.
	rejected, err := bk8s.CreateICMSCreationMessageRequest(ctx,
		decodeCMTask(t, cmsgTaskContainerBYOO), "rhdl-old-rejected", "msgid-rejected", rejectedQueue.QueueURL)
	require.NoError(t, err)
	require.NotEmpty(t, GetCreationRejectionReason(rejected))
	accepted, err := bk8s.CreateICMSCreationMessageRequest(ctx,
		decodeCMTask(t, cmsgTaskContainer), "rhdl-old-accepted", "msgid-accepted", acceptedQueue.QueueURL)
	require.NoError(t, err)
	require.Empty(t, GetCreationRejectionReason(accepted))

	srInfFactory := nvcainformers.NewSharedInformerFactoryWithOptions(clients.BART, ResyncInterval,
		nvcainformers.WithNamespace(bk8s.requestsNamespace))
	bk8s.icmsRequestLister = mustICMSRequestLister(t, srInfFactory)
	srInfFactory.Start(ctx.Done())
	srInfFactory.WaitForCacheSync(ctx.Done())

	// The messages are delivered again with new receipt handles.
	qc := &mockqueue.Client{Use10MillisForWaits: true}
	qc.AddMessage(rejectedQueue.QueueURL, queue.ReceiveMessageOutput{
		MessageID: "msgid-rejected", ReceiptHandle: "rhdl-new-rejected", Body: cmsgTaskContainerBYOO,
	})
	qc.AddMessage(acceptedQueue.QueueURL, queue.ReceiveMessageOutput{
		MessageID: "msgid-accepted", ReceiptHandle: "rhdl-new-accepted", Body: cmsgTaskContainer,
	})

	qm := NewQueueManager(
		bk8s,
		staticStatusGetter{status: types.AgentHealth{
			GPUUsage: types.GPUResourceSet{gpuName: {Capacity: 8}},
		}},
		qc,
		types.QueueCredentials{
			CreationQueues:        map[types.GPUName]queue.MessageQueueInfo{gpuName: rejectedQueue},
			ClusterCreationQueues: map[types.GPUName]queue.MessageQueueInfo{gpuName: acceptedQueue},
			TerminationQueue:      queue.MessageQueueInfo{QueueURL: "termurl", QueueType: queue.TerminationQueue},
		},
		featureflag.DefaultFetcher,
		types.MaintenanceModeNone,
		nvcametrics.FromContext(ctx),
	)
	require.NoError(t, qm.SyncQueues(ctx))

	peek := func(qi queue.MessageQueueInfo) []queue.ReceiveMessageOutput {
		t.Helper()
		msgs, err := qc.PeekMessage(ctx, queue.ReceiveMessageInput{QueueInfo: qi, MaxNumberOfMessages: 1})
		require.NoError(t, err)
		return msgs
	}
	assert.Empty(t, peek(rejectedQueue), "redelivered rejected message should be deleted")
	assert.Len(t, peek(acceptedQueue), 1, "redelivered accepted message should be kept")

	reqs, err := clients.BART.NvcaV2beta1().ICMSRequests(bk8s.requestsNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, reqs.Items, 2)
}

// TestRejectedTaskReportedWithAckAfterPodsScheduled checks that with
// AckTaskRequestAfterPodsScheduled enabled, a rejected task is never
// acknowledged, but sync still fails it and its failure reaches ICMS.
func TestRejectedTaskReportedWithAckAfterPodsScheduled(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	clients := mockKubeClients()
	fff := &featureflagmock.Fetcher{EnabledFFs: []*featureflag.FeatureFlag{
		featureflag.AckTaskRequestAfterPodsScheduled,
	}}

	srInfFactory := nvcainformers.NewSharedInformerFactoryWithOptions(clients.BART, ResyncInterval)
	srLister := mustICMSRequestLister(t, srInfFactory)
	srInfFactory.Start(ctx.Done())
	srInfFactory.WaitForCacheSync(ctx.Done())

	bc := &BackendK8sCache{
		clients:              clients,
		featureFlagFetcher:   fff,
		icmsRequestLister:    srLister,
		icmsRequestWQ:        workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
		requestsNamespace:    RequestsNamespace,
		podInstanceNamespace: RequestsNamespace,
		k8sTimeConfig:        (&k8sutil.TimeConfig{}).Complete(),
		eventRecorder:        record.NewFakeRecorder(100),
		tracer:               noop.NewTracerProvider().Tracer("test"),
	}
	bc.icmsRequestHelper, _ = NewK8sComputeBackend(clients, bc)

	icmsClient, icmsTransport := setupTestICMSClient()
	ag := &Agent{
		AgentOptions:    &AgentOptions{FeatureFlagFetcher: fff},
		backendk8scache: bc,
		icmsClient:      icmsClient,
		queueManager:    &QueueManager{Client: &mockqueue.Client{}},
	}

	sr, err := bc.CreateICMSCreationMessageRequest(ctx,
		decodeCMTask(t, cmsgTaskContainerBYOO), "rhdl", "msgid", "task_queue_url")
	require.NoError(t, err)
	reason := GetCreationRejectionReason(sr)
	require.NotEmpty(t, reason)

	waitForRequestStatus := func(want nvcav2beta1.RequestStatus) *nvcav2beta1.ICMSRequest {
		t.Helper()
		var got *nvcav2beta1.ICMSRequest
		require.EventuallyWithT(t, func(ct *assert.CollectT) {
			req, err := srLister.ICMSRequests(sr.Namespace).Get(sr.Name)
			if assert.NoError(ct, err) {
				assert.Equal(ct, want, req.Status.RequestStatus)
				got = req
			}
		}, 2*time.Second, 50*time.Millisecond)
		return got
	}
	ackRequests := func() {
		t.Helper()
		require.NoError(t, ag.PutICMSRequestAcknowledgement(ctx))
		ag.ackThreadPool.Wait()
		ag.ackThreadPool = nil
	}

	// The new request moves to Pending without an acknowledgement.
	waitForRequestStatus("")
	ackRequests()
	assert.Nil(t, icmsTransport.getRequest())
	waitForRequestStatus(nvcav2beta1.ICMSRequestStatusPending)

	// Sync fails the request and adds placeholder instances to report.
	bc.icmsRequestWQ.Add(makeNamespacedName(sr))
	require.True(t, bc.processICMSRequestWork(ctx))
	failed := waitForRequestStatus(nvcav2beta1.ICMSRequestStatusFailed)
	require.NotZero(t, sr.Spec.CreationMsgInfo.InstanceCount)
	require.Len(t, failed.Status.Instances, int(sr.Spec.CreationMsgInfo.InstanceCount))

	// A failed request is still never acknowledged.
	ackRequests()
	assert.Nil(t, icmsTransport.getRequest())

	// The failure is posted to ICMS as terminated instances with the reason.
	require.NoError(t, ag.PostICMSInstanceRequestStatusUpdates(ctx))
	ag.instStatusThreadPool.Wait()
	posted := icmsTransport.getRequest()
	require.NotNil(t, posted)
	assert.Equal(t, http.MethodPost, posted.Method)
	instanceID := posted.URL.Path[strings.LastIndex(posted.URL.Path, "/")+1:]
	assert.Contains(t, failed.Status.Instances, instanceID)
	assert.True(t, strings.HasSuffix(posted.URL.Path, "/"+sr.Spec.RequestID+"/"+instanceID), posted.URL.Path)
	body, err := posted.GetBody()
	require.NoError(t, err)
	var payload types.ICMSInstanceStatusUpdateRequest
	require.NoError(t, json.NewDecoder(body).Decode(&payload))
	assert.Equal(t, types.ICMSInstanceTerminated, payload.InstanceState)
	assert.Equal(t, types.ICMSInstanceRequestClosed, payload.RequestState)
	assert.Equal(t, types.ICMSInstanceTerminatedTerminalError, payload.TerminationCause)
	assert.Equal(t, reason, payload.HealthInfo.ErrorLog)

	// Every placeholder instance is recorded as reported.
	acked := waitForRequestStatus(nvcav2beta1.ICMSRequestStatusFailureAcknowledged)
	for id, st := range acked.Status.Instances {
		assert.Equal(t, string(types.ICMSInstanceTerminated), st.LastReportedStatus, id)
	}
}

type staticStatusGetter struct {
	status types.AgentHealth
}

func (s staticStatusGetter) GetStatus() types.AgentHealth { return s.status }

func (s staticStatusGetter) RefreshStatus(context.Context) (types.AgentHealth, error) {
	return s.status, nil
}
