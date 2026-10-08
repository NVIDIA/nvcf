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

package operator

import (
	"strings"
	"testing"
	"time"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcabelister "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/listers/nvcf/v1"
	nvcaoperatorerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/errors"
	nvcaopotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/otel"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
)

// caseCollidingMergeConfig is an agent-config-merge payload where a legacy
// uppercase override sits next to the lowercase block generated from chart
// defaults. Before key conflicts were rejected, each decode picked one block
// at random and the operator restarted the agent on every sync.
const caseCollidingMergeConfig = `agent:
  BYOOLogChunking:
    dryRun: false
    exporterBatchMaxSizeBytes: 1000000
    maxBodyBytes: 262144
  byooLogChunking:
    dryRun: false
    maxPayloadBytes: 0
`

const correctedMergeConfig = `agent:
  byooLogChunking:
    dryRun: false
    maxPayloadBytes: 262144
`

func TestGetRawAgentConfigToMerge_KeyConflictIsRecoverable(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{clients: clients, operatorNamespace: NVCAOperatorNamespace}

	_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName},
		Data:       map[string]string{agentConfigFile: caseCollidingMergeConfig},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, found, err := bc.getRawAgentConfigToMerge(ctx)
	require.Error(t, err)
	assert.False(t, found)
	assert.False(t, nvcaoperatorerrors.IsFatal(err))
	assert.True(t, isInvalidAgentConfigError(err))
	assert.EqualError(t, err, `invalid agent-config-merge: read config: config keys "BYOOLogChunking", "byooLogChunking" under "agent" differ only by case (lines 2, 6); keep one spelling`)
}

func TestGetRawAgentConfigToMerge_ExactDuplicateKeyIsRecoverable(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{clients: clients, operatorNamespace: NVCAOperatorNamespace}

	_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName},
		Data:       map[string]string{agentConfigFile: "agent:\n  logLevel: info\n  logLevel: debug\n"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, _, err = bc.getRawAgentConfigToMerge(ctx)
	require.Error(t, err)
	assert.False(t, nvcaoperatorerrors.IsFatal(err))
	assert.True(t, isInvalidAgentConfigError(err))
	assert.ErrorContains(t, err, `config key "logLevel" under "agent" is defined more than once (lines 2, 3)`)
}

// TestSyncNVCFBackend_CaseCollidingMergeConfigDoesNotRollOutAgent covers the
// full lifecycle: a valid install, an update that introduces case-colliding
// keys, repeated syncs that must leave the running agent alone, and recovery
// once the merge config is corrected.
func TestSyncNVCFBackend_CaseCollidingMergeConfigDoesNotRollOutAgent(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClients()
	fakeK8s := clients.K8s.(*fakek8sclient.Clientset)
	backend := getTestNVCFBackendAllFeatures()
	backend.Spec.Overrides = nil
	backend.Spec.ClusterSource = nvcaoptypes.ClusterSourceSelfHosted
	backend.Spec.ClusterConfig.ClusterID = "test-cluster-id"
	backend.Spec.ClusterConfig.ClusterGroupID = "test-cluster-group-id"
	_, err := clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Create(ctx, backend, metav1.CreateOptions{})
	require.NoError(t, err)

	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	require.NoError(t, indexer.Add(backend))
	recorder := record.NewFakeRecorder(200)
	bc := &BackendK8sCache{
		clients:                 clients,
		operatorNamespace:       NVCAOperatorNamespace,
		nvcfBackendLister:       nvcabelister.NewNVCFBackendLister(indexer),
		eventRecorder:           recorder,
		tracer:                  nvcaopotel.NewTracer(),
		ngcServiceKeyFetcher:    &mockTokenFetcher{token: "randomkey"},
		now:                     time.Now,
		enableGXCache:           true,
		clusterSource:           nvcaoptypes.ClusterSourceSelfHosted,
		generateImagePullSecret: false,
	}
	systemNamespace := getSystemNamespace(backend)

	mergeCM, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName, Namespace: NVCAOperatorNamespace},
		Data:       map[string]string{agentConfigFile: "agent:\n  logLevel: info\n"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	require.NoError(t, bc.syncNVCFBackend(ctx, backend, false))

	lastGood, err := clients.K8s.CoreV1().ConfigMaps(systemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	lastGoodDeployment, err := clients.K8s.AppsV1().Deployments(systemNamespace).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)

	// Introduce the colliding keys through the ConfigMap update handler, the
	// path the operator takes when Helm updates agent-config-merge.
	invalid := mergeCM.DeepCopy()
	invalid.Data[agentConfigFile] = caseCollidingMergeConfig
	invalid, err = clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Update(ctx, invalid, metav1.UpdateOptions{})
	require.NoError(t, err)
	fakeK8s.ClearActions()

	err = bc.handleConfigMapUpdate(ctx, mergeCM, invalid)
	require.Error(t, err)
	assert.False(t, nvcaoperatorerrors.IsFatal(err))
	for range 20 {
		err := bc.SyncNVCFBackend(ctx, backend, false)
		require.Error(t, err)
		assert.False(t, nvcaoperatorerrors.IsFatal(err))
		assert.True(t, isInvalidAgentConfigError(err))
		assert.ErrorContains(t, err, `"BYOOLogChunking", "byooLogChunking" under "agent" differ only by case`)
	}

	assert.Empty(t, agentWriteActions(fakeK8s.Actions(), systemNamespace),
		"an invalid merge config must not write the agent ConfigMap or Deployment")
	current, err := clients.K8s.CoreV1().ConfigMaps(systemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, lastGood.Data, current.Data)
	currentDeployment, err := clients.K8s.AppsV1().Deployments(systemNamespace).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, lastGoodDeployment.Spec.Template.Annotations, currentDeployment.Spec.Template.Annotations)

	gotBackend, err := clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, nvidiaiov1.AgentStatusUnhealthy, gotBackend.Status.AgentStatus)
	assert.True(t, hasEventContaining(recorder, "differ only by case"),
		"the NVCFBackend should carry a warning event naming the conflicting keys")

	// Correcting the merge config rolls the agent out once with the new value.
	corrected := invalid.DeepCopy()
	corrected.Data[agentConfigFile] = correctedMergeConfig
	corrected, err = clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Update(ctx, corrected, metav1.UpdateOptions{})
	require.NoError(t, err)
	fakeK8s.ClearActions()
	require.NoError(t, bc.handleConfigMapUpdate(ctx, invalid, corrected))

	assert.Equal(t, 1, countDeploymentUpdates(fakeK8s.Actions(), systemNamespace))
	managed, err := clients.K8s.CoreV1().ConfigMaps(systemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	managedConfig, err := nvcaconfig.DecodeConfig([]byte(managed.Data[agentConfigFile]))
	require.NoError(t, err)
	assert.Equal(t, int64(262144), managedConfig.Agent.BYOOLogChunking.MaxPayloadBytes)

	// Later syncs see the same config and do not restart the agent again.
	fakeK8s.ClearActions()
	for range 20 {
		require.NoError(t, bc.syncNVCFBackend(ctx, backend, false))
	}
	assert.Empty(t, agentWriteActions(fakeK8s.Actions(), systemNamespace),
		"a stable merge config must not roll out the agent again")
}

func agentWriteActions(actions []k8stesting.Action, systemNamespace string) []k8stesting.Action {
	var writes []k8stesting.Action
	for _, action := range actions {
		if action.GetNamespace() != systemNamespace {
			continue
		}
		switch action.GetVerb() {
		case "create", "update", "patch", "delete":
		default:
			continue
		}
		switch action.GetResource().Resource {
		case "deployments":
			writes = append(writes, action)
		case "configmaps":
			if objectName(action) == agentConfigConfigMapName {
				writes = append(writes, action)
			}
		}
	}
	return writes
}

func countDeploymentUpdates(actions []k8stesting.Action, systemNamespace string) int {
	count := 0
	for _, action := range agentWriteActions(actions, systemNamespace) {
		if action.GetResource().Resource == "deployments" && action.GetVerb() == "update" &&
			objectName(action) == nvcaoptypes.NVCAModuleName {
			count++
		}
	}
	return count
}

func objectName(action k8stesting.Action) string {
	switch a := action.(type) {
	case k8stesting.CreateAction:
		if obj, ok := a.GetObject().(metav1.Object); ok {
			return obj.GetName()
		}
	case k8stesting.UpdateAction:
		if obj, ok := a.GetObject().(metav1.Object); ok {
			return obj.GetName()
		}
	case k8stesting.PatchAction:
		return a.GetName()
	case k8stesting.DeleteAction:
		return a.GetName()
	}
	return ""
}

func hasEventContaining(recorder *record.FakeRecorder, substr string) bool {
	for {
		select {
		case event := <-recorder.Events:
			if strings.Contains(event, substr) {
				return true
			}
		default:
			return false
		}
	}
}
