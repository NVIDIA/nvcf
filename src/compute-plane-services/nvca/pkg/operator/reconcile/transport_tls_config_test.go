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
	"context"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/transporttls"
	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcabelister "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/listers/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/cleanup"
	nvcaopotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/otel"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
)

func TestGetAgentConfigToMerge_ResolvesSecretBackedTransportTLS(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{clients: clients, operatorNamespace: NVCAOperatorNamespace}

	_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: nvcaOperatorConfigMapName},
		Data: map[string]string{agentConfigFile: `workload:
  transportTLS:
    trustBundle:
      secretKeyRef:
        name: nvcf-trust
        key: ca.crt
`},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = clients.K8s.CoreV1().Secrets(NVCAOperatorNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nvcf-trust"},
		Data:       map[string][]byte{"ca.crt": []byte(transportTrustTestPEM)},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	cfg, found, err := bc.getAgentConfigToMerge(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, &nvcaconfig.TransportTLSConfig{
		TrustMode:              nvcaconfig.TrustModeBundle,
		TrustBundleFingerprint: "sha256:95b3dc7dfd3212a6f02c644527f0a65890a9a9c80acf7551be6aa89b1f98fe86",
		TrustBundlePEM:         transportTrustTestPEM,
	}, cfg.Workload.TransportTLS)
	assert.NoError(t, transporttls.ValidateConfig(transporttls.NormalizeConfig(*cfg.Workload.TransportTLS)))
}

func TestSecretBackedTransportTLS_UsesIdenticalEncodingAcrossClusterModes(t *testing.T) {
	for _, clusterSource := range []nvcaoptypes.ClusterSource{
		nvcaoptypes.ClusterSourceNGCManaged,
		nvcaoptypes.ClusterSourceHelmManaged,
		nvcaoptypes.ClusterSourceSelfHosted,
	} {
		t.Run(string(clusterSource), func(t *testing.T) {
			ctx := newTestContext()
			clients := mockKubeClientsForIntegrationTests()
			bc := &BackendK8sCache{
				clients:           clients,
				envType:           nvidiaiov1.EnvTypeStage,
				operatorNamespace: NVCAOperatorNamespace,
			}
			createTransportTrustSource(t, ctx, clients)
			setTransportTrustInstalledBundleMountPath(t, ctx, clients, "/nvcf/transport-tls")

			nb := ngcManagedBackendWithAgentConfig(nvidiaiov1.AgentConfig{})
			nb.Spec.ClusterSource = clusterSource
			desiredConfigMap, err := bc.newAgentConfigConfigMap(ctx, nb)
			require.NoError(t, err)
			require.NoError(t, bc.setupAgentConfigConfigMap(ctx, desiredConfigMap))

			storedConfig, err := clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
			require.NoError(t, err)
			decodedConfig, err := nvcaconfig.DecodeConfig([]byte(storedConfig.Data[agentConfigFile]))
			require.NoError(t, err)
			require.NotNil(t, decodedConfig.Workload.TransportTLS)
			assert.Equal(t, nvcaconfig.TrustModeBundle, decodedConfig.Workload.TransportTLS.TrustMode)
			assert.Equal(t, transportTrustTestPEM, decodedConfig.Workload.TransportTLS.TrustBundlePEM)
			assert.Equal(t, "/nvcf/transport-tls", decodedConfig.Workload.TransportTLS.InstalledBundleMountPath)

			checker, err := bc.newAgentConfigChangedCheck(ctx, nb, desiredConfigMap)
			require.NoError(t, err)
			assert.False(t, checker(), "setup and rollout comparison must encode the same configuration")
		})
	}
}

func TestSecretBackedTransportTLS_InstalledBundleMountPathChangeTriggersAgentRollout(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:           clients,
		envType:           nvidiaiov1.EnvTypeStage,
		operatorNamespace: NVCAOperatorNamespace,
	}
	createTransportTrustSource(t, ctx, clients)
	nb := ngcManagedBackendWithAgentConfig(nvidiaiov1.AgentConfig{})
	nb.Spec.NVCAImageConfig = nvidiaiov1.ImageConfig{Repository: "registry.example.test/nvca", Tag: "2.52.0"}
	initialConfigMap, err := bc.newAgentConfigConfigMap(ctx, nb)
	require.NoError(t, err)
	require.NoError(t, bc.setupAgentConfigConfigMap(ctx, initialConfigMap))

	setTransportTrustInstalledBundleMountPath(t, ctx, clients, "/nvcf/transport-tls")
	desiredConfigMap, err := bc.newAgentConfigConfigMap(ctx, nb)
	require.NoError(t, err)
	checker, err := bc.newAgentConfigChangedCheck(ctx, nb, desiredConfigMap)
	require.NoError(t, err)
	assert.True(t, checker())
	require.NoError(t, bc.setupAgentConfigConfigMap(ctx, desiredConfigMap))

	storedConfig, err := clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Get(ctx, agentConfigConfigMapName,
		metav1.GetOptions{})
	require.NoError(t, err)
	decodedConfig, err := nvcaconfig.DecodeConfig([]byte(storedConfig.Data[agentConfigFile]))
	require.NoError(t, err)
	require.NotNil(t, decodedConfig.Workload.TransportTLS)
	assert.Equal(t, "/nvcf/transport-tls", decodedConfig.Workload.TransportTLS.InstalledBundleMountPath)
}

func TestGetAgentConfigToMerge_RejectsTransportTLSSourceConflict(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{clients: clients, operatorNamespace: NVCAOperatorNamespace}

	mergeCfg := nvcaconfig.Config{
		Workload: nvcaconfig.WorkloadConfig{
			TransportTLS: &nvcaconfig.TransportTLSConfig{TrustMode: nvcaconfig.TrustModeSystem},
		},
	}
	mergeConfigData, err := nvcaconfig.EncodeConfig(mergeCfg)
	require.NoError(t, err)
	_, err = clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName},
		Data:       map[string]string{agentConfigFile: string(mergeConfigData)},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	createTransportTrustSource(t, ctx, clients)

	_, _, err = bc.getAgentConfigToMerge(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both configure workload.transportTLS")
}

func TestSecretBackedTransportTLS_RotationUpdatesAgentConfigWithoutMutatingWorkers(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:           clients,
		envType:           nvidiaiov1.EnvTypeStage,
		operatorNamespace: NVCAOperatorNamespace,
	}
	createTransportTrustSource(t, ctx, clients)
	_, err := clients.K8s.CoreV1().Pods(DefaultNVCASystemNamespace).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "existing-worker"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	workerBeforeRotation, err := clients.K8s.CoreV1().Pods(DefaultNVCASystemNamespace).Get(ctx, "existing-worker", metav1.GetOptions{})
	require.NoError(t, err)

	nb := ngcManagedBackendWithAgentConfig(nvidiaiov1.AgentConfig{})
	nb.Spec.NVCAImageConfig = nvidiaiov1.ImageConfig{Repository: "registry.example.test/nvca", Tag: "2.52.0"}
	initialConfigMap, err := bc.newAgentConfigConfigMap(ctx, nb)
	require.NoError(t, err)
	require.NoError(t, bc.setupAgentConfigConfigMap(ctx, initialConfigMap))
	beforeRotation, err := clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	beforeRotationData := beforeRotation.Data[agentConfigFile]

	secret, err := clients.K8s.CoreV1().Secrets(NVCAOperatorNamespace).Get(ctx, "nvcf-trust", metav1.GetOptions{})
	require.NoError(t, err)
	secret.Data["ca.crt"] = []byte(transportTrustTestPEM + transportTrustTestPEM)
	_, err = clients.K8s.CoreV1().Secrets(NVCAOperatorNamespace).Update(ctx, secret, metav1.UpdateOptions{})
	require.NoError(t, err)

	desiredConfigMap, err := bc.newAgentConfigConfigMap(ctx, nb)
	require.NoError(t, err)
	checker, err := bc.newAgentConfigChangedCheck(ctx, nb, desiredConfigMap)
	require.NoError(t, err)
	assert.True(t, checker())
	require.NoError(t, bc.setupAgentConfigConfigMap(ctx, desiredConfigMap))

	afterRotation, err := clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotEqual(t, beforeRotationData, afterRotation.Data[agentConfigFile])
	workerAfterRotation, err := clients.K8s.CoreV1().Pods(DefaultNVCASystemNamespace).Get(ctx, "existing-worker", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, workerBeforeRotation, workerAfterRotation)
}

func TestSecretBackedTransportTLS_SecretRotationUsesSameConfigForCheckAndWrite(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:           clients,
		envType:           nvidiaiov1.EnvTypeStage,
		operatorNamespace: NVCAOperatorNamespace,
	}
	createTransportTrustSource(t, ctx, clients)
	nb := ngcManagedBackendWithAgentConfig(nvidiaiov1.AgentConfig{})

	initialConfigMap, err := bc.newAgentConfigConfigMap(ctx, nb)
	require.NoError(t, err)
	require.NoError(t, bc.setupAgentConfigConfigMap(ctx, initialConfigMap))

	secret, err := clients.K8s.CoreV1().Secrets(NVCAOperatorNamespace).Get(ctx, "nvcf-trust", metav1.GetOptions{})
	require.NoError(t, err)
	secret.Data["ca.crt"] = []byte(transportTrustTestPEM + transportTrustTestPEM)
	_, err = clients.K8s.CoreV1().Secrets(NVCAOperatorNamespace).Update(ctx, secret, metav1.UpdateOptions{})
	require.NoError(t, err)

	desiredConfigMap, err := bc.newAgentConfigConfigMap(ctx, nb)
	require.NoError(t, err)
	checker, err := bc.newAgentConfigChangedCheck(ctx, nb, desiredConfigMap)
	require.NoError(t, err)
	assert.True(t, checker())

	secret, err = clients.K8s.CoreV1().Secrets(NVCAOperatorNamespace).Get(ctx, "nvcf-trust", metav1.GetOptions{})
	require.NoError(t, err)
	secret.Data["ca.crt"] = []byte(transportTrustTestPEM + transportTrustTestPEM + transportTrustTestPEM)
	_, err = clients.K8s.CoreV1().Secrets(NVCAOperatorNamespace).Update(ctx, secret, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.NoError(t, bc.setupAgentConfigConfigMap(ctx, desiredConfigMap))
	storedConfigMap, err := clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Get(ctx, agentConfigConfigMapName,
		metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, desiredConfigMap.Data[agentConfigFile], storedConfigMap.Data[agentConfigFile])
}

func TestNewAgentConfigConfigMap_SecretTrustFailurePreservesLastGoodConfig(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:           clients,
		envType:           nvidiaiov1.EnvTypeStage,
		operatorNamespace: NVCAOperatorNamespace,
	}
	createTransportTrustSource(t, ctx, clients)
	nb := ngcManagedBackendWithAgentConfig(nvidiaiov1.AgentConfig{})
	initialConfigMap, err := bc.newAgentConfigConfigMap(ctx, nb)
	require.NoError(t, err)
	require.NoError(t, bc.setupAgentConfigConfigMap(ctx, initialConfigMap))

	lastGoodConfig, err := clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	lastGoodData := lastGoodConfig.Data[agentConfigFile]

	secret, err := clients.K8s.CoreV1().Secrets(NVCAOperatorNamespace).Get(ctx, "nvcf-trust", metav1.GetOptions{})
	require.NoError(t, err)
	secret.Data["ca.crt"] = []byte("not a certificate")
	_, err = clients.K8s.CoreV1().Secrets(NVCAOperatorNamespace).Update(ctx, secret, metav1.UpdateOptions{})
	require.NoError(t, err)

	_, err = bc.newAgentConfigConfigMap(ctx, nb)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains non-PEM data")

	storedConfig, err := clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, lastGoodData, storedConfig.Data[agentConfigFile])
}

func TestNewAgentConfigConfigMap_InvalidInstalledBundleMountPathPreservesLastGoodConfig(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:           clients,
		envType:           nvidiaiov1.EnvTypeStage,
		operatorNamespace: NVCAOperatorNamespace,
	}
	createTransportTrustSource(t, ctx, clients)
	nb := ngcManagedBackendWithAgentConfig(nvidiaiov1.AgentConfig{})
	initialConfigMap, err := bc.newAgentConfigConfigMap(ctx, nb)
	require.NoError(t, err)
	require.NoError(t, bc.setupAgentConfigConfigMap(ctx, initialConfigMap))

	lastGoodConfig, err := clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Get(ctx, agentConfigConfigMapName,
		metav1.GetOptions{})
	require.NoError(t, err)
	lastGoodData := lastGoodConfig.Data[agentConfigFile]

	operatorConfig, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Get(ctx, nvcaOperatorConfigMapName,
		metav1.GetOptions{})
	require.NoError(t, err)
	operatorConfig.Data[agentConfigFile] = `workload:
  transportTLS:
    trustBundle:
      secretKeyRef:
        name: nvcf-trust
        key: ca.crt
    installedBundleMountPath: nvcf/transport-tls
`
	_, err = clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Update(ctx, operatorConfig, metav1.UpdateOptions{})
	require.NoError(t, err)

	_, err = bc.newAgentConfigConfigMap(ctx, nb)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "installedBundleMountPath must be absolute")

	storedConfig, err := clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Get(ctx, agentConfigConfigMapName,
		metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, lastGoodData, storedConfig.Data[agentConfigFile])
}

func TestConfigMapAddHandler_SkipsInitialListForOperatorConfig(t *testing.T) {
	ctx := newTestContext()
	bc, backend := newConfigMapEventTestCache(t, ctx)

	err := bc.handleConfigMapAdd(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: nvcaOperatorConfigMapName},
	})
	require.NoError(t, err)

	storedBackend, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, storedBackend.Finalizers, cleanup.NVCAOperatorFinalizer)
}

func TestSyncCurrentBackendForConfigMapChange_WrapsError(t *testing.T) {
	ctx := newTestContext()
	bc, _ := newConfigMapEventTestCache(t, ctx)

	err := bc.syncCurrentBackendForConfigMapChange(ctx, logrus.NewEntry(logrus.New()))

	require.EqualError(t, err, "sync current NVCFBackend: event-backend version cannot be empty")
}

func TestConfigMapUpdateHandler_ReconcilesWhenOperatorConfigDataChanges(t *testing.T) {
	ctx := newTestContext()
	bc, backend := newConfigMapEventTestCache(t, ctx)

	err := bc.handleConfigMapUpdate(ctx,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: nvcaOperatorConfigMapName}, Data: map[string]string{agentConfigFile: "before"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: nvcaOperatorConfigMapName}, Data: map[string]string{agentConfigFile: "after"}},
	)
	require.ErrorContains(t, err, "version cannot be empty")

	storedBackend, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, storedBackend.Finalizers, cleanup.NVCAOperatorFinalizer)
}

func TestConfigMapUpdateHandler_SkipsUnchangedOperatorConfig(t *testing.T) {
	ctx := newTestContext()
	bc, backend := newConfigMapEventTestCache(t, ctx)

	err := bc.handleConfigMapUpdate(ctx,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: nvcaOperatorConfigMapName}, Data: map[string]string{agentConfigFile: "unchanged"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: nvcaOperatorConfigMapName}, Data: map[string]string{agentConfigFile: "unchanged"}},
	)
	require.NoError(t, err)

	storedBackend, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, storedBackend.Finalizers, cleanup.NVCAOperatorFinalizer)
}

func TestConfigMapChangesForceNVCAReconcile(t *testing.T) {
	assert.True(t, configMapUpdateForcesNVCAReconcile(nvcaOperatorConfigMapName))
	assert.True(t, configMapUpdateForcesNVCAReconcile(nvcfBackendChartDefaultsConfigMapName))
	assert.False(t, configMapUpdateForcesNVCAReconcile("unrelated-configmap"))
}

func newConfigMapEventTestCache(t *testing.T, ctx context.Context) (*BackendK8sCache, *nvidiaiov1.NVCFBackend) {
	t.Helper()
	clients := mockKubeClientsForIntegrationTests()
	backend := ngcManagedBackendWithAgentConfig(nvidiaiov1.AgentConfig{})
	backend.Name = "event-backend"
	backend.Namespace = NVCAOperatorNamespace
	_, err := clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Create(ctx, backend, metav1.CreateOptions{})
	require.NoError(t, err)

	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	require.NoError(t, indexer.Add(backend))
	return &BackendK8sCache{
		clients:                 clients,
		operatorNamespace:       NVCAOperatorNamespace,
		nvcfBackendLister:       nvcabelister.NewNVCFBackendLister(indexer),
		eventRecorder:           record.NewFakeRecorder(10),
		tracer:                  nvcaopotel.NewTracer(),
		generateImagePullSecret: false,
	}, backend
}
