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
	"os"
	"testing"
	"time"

	nvcaenvtest "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/envtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/transporttls"
	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcabelister "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/listers/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/cleanup"
	nvcaoperatorerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
	nvcaopotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/otel"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
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

func TestGetRawAgentConfigToMerge_MissingReturnsNotFound(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{clients: clients, operatorNamespace: NVCAOperatorNamespace}

	cfg, found, err := bc.getRawAgentConfigToMerge(ctx)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, nvcaconfig.Config{}, cfg)
}

func TestGetRawAgentConfigToMerge_MalformedIsFatal(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{clients: clients, operatorNamespace: NVCAOperatorNamespace}

	_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName},
		Data:       map[string]string{agentConfigFile: "workload: ["},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, found, err := bc.getRawAgentConfigToMerge(ctx)
	require.Error(t, err)
	assert.False(t, found)
	assert.True(t, nvcaoperatorerrors.IsFatal(err))
	assert.Contains(t, err.Error(), "invalid agent-config-merge")
}

func TestGetAgentConfigToMerge_RejectsBundleWithQUICInsecureAsFatal(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{clients: clients, operatorNamespace: NVCAOperatorNamespace}

	_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName},
		Data: map[string]string{agentConfigFile: `workload:
  stargateQUICInsecure: true
  transportTLS:
    trustMode: bundle
`},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, _, err = bc.getAgentConfigToMerge(ctx)
	require.Error(t, err)
	assert.True(t, nvcaoperatorerrors.IsFatal(err), "invalid static agent configuration must not be requeued")
	assert.Contains(t, err.Error(), "workload.stargateQUICInsecure=true cannot be used with workload.transportTLS.trustMode=bundle")
	assert.Contains(t, err.Error(), "set workload.stargateQUICInsecure=false or use trustMode=system")
}

func TestGetAgentConfigToMerge_RejectsSecretBundleWithQUICInsecureAsFatal(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{clients: clients, operatorNamespace: NVCAOperatorNamespace}

	_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName},
		Data: map[string]string{agentConfigFile: `workload:
  stargateQUICInsecure: true
`},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	createTransportTrustSource(t, ctx, clients)

	_, _, err = bc.getAgentConfigToMerge(ctx)
	require.Error(t, err)
	assert.True(t, nvcaoperatorerrors.IsFatal(err), "invalid combined operator configuration must not be requeued")
	assert.Contains(t, err.Error(), "workload.stargateQUICInsecure=true cannot be used with workload.transportTLS.trustMode=bundle")
}

func TestGetAgentConfigToMerge_AllowsSystemWithQUICInsecure(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{clients: clients, operatorNamespace: NVCAOperatorNamespace}

	_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName},
		Data: map[string]string{agentConfigFile: `workload:
  stargateQUICInsecure: true
  transportTLS:
    trustMode: system
`},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	cfg, found, err := bc.getAgentConfigToMerge(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, cfg.Workload.StargateQUICInsecure)
	require.NotNil(t, cfg.Workload.TransportTLS)
	assert.Equal(t, nvcaconfig.TrustModeSystem, cfg.Workload.TransportTLS.TrustMode)
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
	bc.syncedFuncs = []cache.InformerSynced{func() bool { return false }}
	bc.configMapHandlerRegistration = testResourceEventHandlerRegistration{synced: false}

	err := bc.handleConfigMapAdd(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: nvcaOperatorConfigMapName},
	})
	require.NoError(t, err)

	storedBackend, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, storedBackend.Finalizers, cleanup.NVCAOperatorFinalizer)
}

func TestConfigMapAddHandler_RejectsInvalidObjectWithContext(t *testing.T) {
	ctx := newTestContext()
	bc, _ := newConfigMapEventTestCache(t, ctx)

	err := bc.handleConfigMapAdd(ctx, &corev1.Secret{})

	require.EqualError(t, err, "invalid object received in ConfigMap Add handler: *v1.Secret")
}

func TestConfigMapAddHandler_SkipsInitialListAfterInformerCacheSync(t *testing.T) {
	ctx := newTestContext()
	bc, backend := newConfigMapEventTestCache(t, ctx)
	bc.syncedFuncs = []cache.InformerSynced{func() bool { return true }}
	bc.configMapHandlerRegistration = testResourceEventHandlerRegistration{synced: false}

	err := bc.handleConfigMapAdd(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: nvcaOperatorConfigMapName},
	})
	require.NoError(t, err)

	storedBackend, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, storedBackend.Finalizers, cleanup.NVCAOperatorFinalizer)
}

func TestConfigMapAddHandler_SkipsRecreatedOperatorConfigUntilAllInformersSync(t *testing.T) {
	ctx := newTestContext()
	bc, backend := newConfigMapEventTestCache(t, ctx)
	bc.syncedFuncs = []cache.InformerSynced{func() bool { return false }}
	bc.configMapHandlerRegistration = testResourceEventHandlerRegistration{synced: true}

	err := bc.handleConfigMapAdd(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: nvcaOperatorConfigMapName},
	})
	require.NoError(t, err)

	storedBackend, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, storedBackend.Finalizers, cleanup.NVCAOperatorFinalizer)
}

func TestConfigMapAddHandler_ReconcilesRecreatedOperatorConfigAfterSync(t *testing.T) {
	ctx := newTestContext()
	bc, backend := newConfigMapEventTestCache(t, ctx)
	bc.syncedFuncs = []cache.InformerSynced{func() bool { return true }}
	bc.configMapHandlerRegistration = testResourceEventHandlerRegistration{synced: true}

	err := bc.handleConfigMapAdd(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: nvcaOperatorConfigMapName},
	})
	require.ErrorContains(t, err, "version cannot be empty")

	storedBackend, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, storedBackend.Finalizers, cleanup.NVCAOperatorFinalizer)
}

func TestConfigMapAddHandler_DispatchesRecreatedBackendConfigAfterSync(t *testing.T) {
	ctx := newTestContext()
	bc, _ := newConfigMapEventTestCache(t, ctx)
	bc.clusterSource = nvcaoptypes.ClusterSourceHelmManaged
	bc.syncedFuncs = []cache.InformerSynced{func() bool { return true }}
	bc.configMapHandlerRegistration = testResourceEventHandlerRegistration{synced: true}
	dispatches := 0
	bc.dispatchReconcileClusterFunc = func(context.Context) { dispatches++ }

	err := bc.handleConfigMapAdd(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: nvcfBackendHelmManagedConfigMapName},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, dispatches)
}

func TestConfigMapAddHandler_SkipsInactiveBackendConfig(t *testing.T) {
	ctx := newTestContext()
	bc, _ := newConfigMapEventTestCache(t, ctx)
	bc.clusterSource = nvcaoptypes.ClusterSourceSelfHosted
	bc.syncedFuncs = []cache.InformerSynced{func() bool { return true }}
	bc.configMapHandlerRegistration = testResourceEventHandlerRegistration{synced: true}
	dispatches := 0
	bc.dispatchReconcileClusterFunc = func(context.Context) { dispatches++ }

	err := bc.handleConfigMapAdd(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: nvcfBackendHelmManagedConfigMapName},
	})
	require.NoError(t, err)
	assert.Zero(t, dispatches)
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

func TestConfigMapUpdateHandler_ReconcilesWhenAgentMergeConfigDataChanges(t *testing.T) {
	ctx := newTestContext()
	bc, backend := newConfigMapEventTestCache(t, ctx)

	err := bc.handleConfigMapUpdate(ctx,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName}, Data: map[string]string{agentConfigFile: "before"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName}, Data: map[string]string{agentConfigFile: "after"}},
	)
	require.ErrorContains(t, err, "version cannot be empty")

	storedBackend, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, storedBackend.Finalizers, cleanup.NVCAOperatorFinalizer)
}

func TestConfigMapUpdateHandler_PropagatesAgentMergeConfigToExistingBackend(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClients()
	backend := getTestNVCFBackendAllFeatures()
	backend.UID = "existing-backend-uid"
	backend.Spec.Overrides = nil
	backend.Spec.ClusterSource = nvcaoptypes.ClusterSourceSelfHosted
	backend.Spec.ClusterConfig.ClusterID = "existing-cluster-id"
	backend.Spec.ClusterConfig.ClusterGroupID = "existing-cluster-group-id"
	_, err := clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Create(ctx, backend, metav1.CreateOptions{})
	require.NoError(t, err)

	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	require.NoError(t, indexer.Add(backend))
	bc := &BackendK8sCache{
		clients:                 clients,
		operatorNamespace:       NVCAOperatorNamespace,
		nvcfBackendLister:       nvcabelister.NewNVCFBackendLister(indexer),
		eventRecorder:           record.NewFakeRecorder(20),
		tracer:                  nvcaopotel.NewTracer(),
		ngcServiceKeyFetcher:    &mockTokenFetcher{token: "randomkey"},
		now:                     time.Now,
		enableGXCache:           true,
		clusterSource:           nvcaoptypes.ClusterSourceSelfHosted,
		generateImagePullSecret: false,
	}

	before := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName, Namespace: NVCAOperatorNamespace},
		Data: map[string]string{agentConfigFile: `workload:
  stargateQUICInsecure: false
`},
	}
	before, err = clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, before, metav1.CreateOptions{})
	require.NoError(t, err)
	require.NoError(t, bc.syncNVCFBackend(ctx, backend, false))

	after := before.DeepCopy()
	after.Data[agentConfigFile] = `workload:
  stargateQUICInsecure: true
`
	after, err = clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Update(ctx, after, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.NoError(t, bc.handleConfigMapUpdate(ctx, before, after))

	managed, err := clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	managedConfig, err := nvcaconfig.DecodeConfig([]byte(managed.Data[agentConfigFile]))
	require.NoError(t, err)
	assert.True(t, managedConfig.Workload.StargateQUICInsecure)

	require.NoError(t, clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Delete(ctx, agentConfigMergeConfigMapName, metav1.DeleteOptions{}))
	require.NoError(t, bc.handleConfigMapDelete(ctx, after))
	managed, err = clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	managedConfig, err = nvcaconfig.DecodeConfig([]byte(managed.Data[agentConfigFile]))
	require.NoError(t, err)
	assert.False(t, managedConfig.Workload.StargateQUICInsecure)
	lastGoodData := managed.Data[agentConfigFile]

	_, err = clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName, Namespace: NVCAOperatorNamespace},
		Data:       map[string]string{agentConfigFile: "workload: ["},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	err = bc.syncNVCFBackend(ctx, backend, false)
	require.Error(t, err)
	assert.True(t, nvcaoperatorerrors.IsFatal(err))
	managed, err = clients.K8s.CoreV1().ConfigMaps(DefaultNVCASystemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, lastGoodData, managed.Data[agentConfigFile])

	storedBackend, err := clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, backend.UID, storedBackend.UID)
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

func TestConfigMapUpdateHandler_DispatchesSelfHostedBackendConfigDataChanges(t *testing.T) {
	ctx := newTestContext()
	bc, _ := newConfigMapEventTestCache(t, ctx)
	bc.clusterSource = nvcaoptypes.ClusterSourceSelfHosted
	dispatches := 0
	bc.dispatchReconcileClusterFunc = func(context.Context) { dispatches++ }

	err := bc.handleConfigMapUpdate(ctx,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: nvcfBackendSelfManagedConfigMapName}, Data: map[string]string{"cluster-dto.yaml": "before"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: nvcfBackendSelfManagedConfigMapName}, Data: map[string]string{"cluster-dto.yaml": "after"}},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, dispatches)
}

func TestConfigMapUpdateHandler_SkipsUnchangedSelfHostedBackendConfig(t *testing.T) {
	ctx := newTestContext()
	bc, _ := newConfigMapEventTestCache(t, ctx)
	bc.clusterSource = nvcaoptypes.ClusterSourceSelfHosted
	dispatches := 0
	bc.dispatchReconcileClusterFunc = func(context.Context) { dispatches++ }

	err := bc.handleConfigMapUpdate(ctx,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: nvcfBackendSelfManagedConfigMapName}, Data: map[string]string{"cluster-dto.yaml": "unchanged"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: nvcfBackendSelfManagedConfigMapName}, Data: map[string]string{"cluster-dto.yaml": "unchanged"}},
	)
	require.NoError(t, err)
	assert.Zero(t, dispatches)
}

func TestConfigMapUpdateHandler_SkipsInactiveBackendConfig(t *testing.T) {
	ctx := newTestContext()
	bc, _ := newConfigMapEventTestCache(t, ctx)
	bc.clusterSource = nvcaoptypes.ClusterSourceSelfHosted
	dispatches := 0
	bc.dispatchReconcileClusterFunc = func(context.Context) { dispatches++ }

	err := bc.handleConfigMapUpdate(ctx,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: nvcfBackendHelmManagedConfigMapName}, Data: map[string]string{"cluster-dto.yaml": "before"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: nvcfBackendHelmManagedConfigMapName}, Data: map[string]string{"cluster-dto.yaml": "after"}},
	)
	require.NoError(t, err)
	assert.Zero(t, dispatches)
}

func TestConfigMapUpdateHandler_SkipsUnrelatedConfigMap(t *testing.T) {
	ctx := newTestContext()
	bc, backend := newConfigMapEventTestCache(t, ctx)
	dispatches := 0
	bc.dispatchReconcileClusterFunc = func(context.Context) { dispatches++ }

	err := bc.handleConfigMapUpdate(ctx,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "unrelated-configmap"}, Data: map[string]string{"value": "before"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "unrelated-configmap"}, Data: map[string]string{"value": "after"}},
	)
	require.NoError(t, err)
	assert.Zero(t, dispatches)

	storedBackend, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, storedBackend.Finalizers, cleanup.NVCAOperatorFinalizer)
}

func TestConfigMapDeleteHandler_ReconcilesDeletedAgentMergeConfig(t *testing.T) {
	ctx := newTestContext()
	bc, backend := newConfigMapEventTestCache(t, ctx)

	err := bc.handleConfigMapDelete(ctx, cache.DeletedFinalStateUnknown{Obj: &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName},
	}})
	require.ErrorContains(t, err, "version cannot be empty")

	storedBackend, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, storedBackend.Finalizers, cleanup.NVCAOperatorFinalizer)
}

func TestConfigMapDeleteHandler_DispatchesDeletedSelfHostedBackendConfig(t *testing.T) {
	ctx := newTestContext()
	bc, _ := newConfigMapEventTestCache(t, ctx)
	bc.clusterSource = nvcaoptypes.ClusterSourceSelfHosted
	dispatches := 0
	bc.dispatchReconcileClusterFunc = func(context.Context) { dispatches++ }

	err := bc.handleConfigMapDelete(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: nvcfBackendSelfManagedConfigMapName},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, dispatches)
}

func TestConfigMapDeleteHandler_SkipsInactiveBackendConfig(t *testing.T) {
	ctx := newTestContext()
	bc, _ := newConfigMapEventTestCache(t, ctx)
	bc.clusterSource = nvcaoptypes.ClusterSourceSelfHosted
	dispatches := 0
	bc.dispatchReconcileClusterFunc = func(context.Context) { dispatches++ }

	err := bc.handleConfigMapDelete(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: nvcfBackendHelmManagedConfigMapName},
	})
	require.NoError(t, err)
	assert.Zero(t, dispatches)
}

func TestBackendConfigMapMatchesClusterSource(t *testing.T) {
	tests := []struct {
		name          string
		clusterSource nvcaoptypes.ClusterSource
		configMapName string
		want          bool
	}{
		{name: "helm source and helm config", clusterSource: nvcaoptypes.ClusterSourceHelmManaged, configMapName: nvcfBackendHelmManagedConfigMapName, want: true},
		{name: "helm source and self-hosted config", clusterSource: nvcaoptypes.ClusterSourceHelmManaged, configMapName: nvcfBackendSelfManagedConfigMapName},
		{name: "self-hosted source and self-hosted config", clusterSource: nvcaoptypes.ClusterSourceSelfHosted, configMapName: nvcfBackendSelfManagedConfigMapName, want: true},
		{name: "self-hosted source and helm config", clusterSource: nvcaoptypes.ClusterSourceSelfHosted, configMapName: nvcfBackendHelmManagedConfigMapName},
		{name: "NGC source and helm config", clusterSource: nvcaoptypes.ClusterSourceNGCManaged, configMapName: nvcfBackendHelmManagedConfigMapName},
		{name: "self-hosted source and unrelated config", clusterSource: nvcaoptypes.ClusterSourceSelfHosted, configMapName: "unrelated-configmap"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{clusterSource: tt.clusterSource}
			assert.Equal(t, tt.want, bc.isBackendConfigMapMatchesClusterSource(tt.configMapName))
		})
	}
}

func TestConfigMapInformerEnvtest_DispatchesOnlyActiveSelfHostedSource(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is required for envtest")
	}

	cfg, k8sClient, cleanupEnvtest, err := nvcaenvtest.SetupEnvtest()
	require.NoError(t, err)
	t.Cleanup(cleanupEnvtest)

	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)
	const namespace = "configmap-informer-envtest"
	_, err = k8sClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	for _, name := range []string{nvcfBackendHelmManagedConfigMapName, nvcfBackendSelfManagedConfigMapName} {
		_, err = k8sClient.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Data:       map[string]string{"cluster-dto.yaml": "before"},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	dispatched := make(chan struct{}, 2)
	bc := &BackendK8sCache{
		clients:                      &kubeclients.KubeClients{Config: cfg, K8s: k8sClient},
		operatorNamespace:            namespace,
		clusterSource:                nvcaoptypes.ClusterSourceSelfHosted,
		tracer:                       nvcaopotel.NewTracer(),
		dispatchReconcileClusterFunc: func(context.Context) { dispatched <- struct{}{} },
	}
	require.NoError(t, addConfigMapInformers(ctx, bc))
	require.Eventually(t, func() bool {
		return bc.informersSynced() && bc.configMapHandlerRegistration.HasSynced()
	}, 5*time.Second, 20*time.Millisecond)

	updateConfigMap := func(name string) {
		cm, getErr := k8sClient.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
		require.NoError(t, getErr)
		cm.Data["cluster-dto.yaml"] = "after"
		_, updateErr := k8sClient.CoreV1().ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{})
		require.NoError(t, updateErr)
	}
	assertNotDispatched := func() {
		assert.Never(t, func() bool {
			select {
			case <-dispatched:
				return true
			default:
				return false
			}
		}, 300*time.Millisecond, 20*time.Millisecond)
	}
	assertDispatched := func() {
		select {
		case <-dispatched:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for cluster reconcile dispatch")
		}
	}

	updateConfigMap(nvcfBackendHelmManagedConfigMapName)
	assertNotDispatched()
	updateConfigMap(nvcfBackendSelfManagedConfigMapName)
	assertDispatched()

	require.NoError(t, k8sClient.CoreV1().ConfigMaps(namespace).Delete(ctx, nvcfBackendHelmManagedConfigMapName, metav1.DeleteOptions{}))
	assertNotDispatched()
	require.NoError(t, k8sClient.CoreV1().ConfigMaps(namespace).Delete(ctx, nvcfBackendSelfManagedConfigMapName, metav1.DeleteOptions{}))
	assertDispatched()
}

func TestConfigMapDeleteHandler_SkipsUnrelatedConfigMap(t *testing.T) {
	ctx := newTestContext()
	bc, backend := newConfigMapEventTestCache(t, ctx)

	err := bc.handleConfigMapDelete(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "unrelated-configmap"},
	})
	require.NoError(t, err)

	storedBackend, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, storedBackend.Finalizers, cleanup.NVCAOperatorFinalizer)
}

func TestSyncNVCFBackend_WrapsDesiredAgentConfigError(t *testing.T) {
	ctx := newTestContext()
	bc, backend := newConfigMapEventTestCache(t, ctx)
	bc.functionEnvOverridesB64 = "not-base64"

	storedBackend, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Get(ctx, backend.Name, metav1.GetOptions{})
	require.NoError(t, err)
	storedBackend.Spec.Version = "1.0.0"
	_, err = bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).Update(ctx, storedBackend, metav1.UpdateOptions{})
	require.NoError(t, err)

	err = bc.syncNVCFBackend(ctx, backend, false)
	require.ErrorContains(t, err, "create desired agent config ConfigMap:")
	require.ErrorContains(t, err, "decode function env overrides")
}

func TestConfigMapChangesForceNVCAReconcile(t *testing.T) {
	assert.True(t, configMapUpdateForcesNVCAReconcile(nvcaOperatorConfigMapName))
	assert.True(t, configMapUpdateForcesNVCAReconcile(nvcfBackendChartDefaultsConfigMapName))
	assert.True(t, configMapUpdateForcesNVCAReconcile(agentConfigMergeConfigMapName))
	assert.True(t, configMapUpdateForcesNVCAReconcile(nvcastorage.StorageCapabilityConfigMapName))
	assert.False(t, configMapUpdateForcesNVCAReconcile("unrelated-configmap"))
}

func TestStorageCapabilityConfigMapEventsForceNVCAReconcile(t *testing.T) {
	for _, tt := range []struct {
		name      string
		handle    func(context.Context, *BackendK8sCache) error
		wantForce bool
	}{
		{
			name: "add after informer sync",
			handle: func(ctx context.Context, bc *BackendK8sCache) error {
				bc.syncedFuncs = []cache.InformerSynced{func() bool { return true }}
				bc.configMapHandlerRegistration = testResourceEventHandlerRegistration{synced: true}
				return bc.handleConfigMapAdd(ctx, &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: nvcastorage.StorageCapabilityConfigMapName},
				})
			},
			wantForce: true,
		},
		{
			name: "changed update",
			handle: func(ctx context.Context, bc *BackendK8sCache) error {
				return bc.handleConfigMapUpdate(ctx,
					&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: nvcastorage.StorageCapabilityConfigMapName},
						Data:       map[string]string{nvcastorage.StorageCapabilityConfigMapKey: "before"},
					},
					&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: nvcastorage.StorageCapabilityConfigMapName},
						Data:       map[string]string{nvcastorage.StorageCapabilityConfigMapKey: "after"},
					},
				)
			},
			wantForce: true,
		},
		{
			name: "delete",
			handle: func(ctx context.Context, bc *BackendK8sCache) error {
				return bc.handleConfigMapDelete(ctx, cache.DeletedFinalStateUnknown{
					Obj: &corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: nvcastorage.StorageCapabilityConfigMapName},
					},
				})
			},
			wantForce: true,
		},
		{
			name: "unchanged update",
			handle: func(ctx context.Context, bc *BackendK8sCache) error {
				unchanged := map[string]string{nvcastorage.StorageCapabilityConfigMapKey: "same"}
				return bc.handleConfigMapUpdate(ctx,
					&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: nvcastorage.StorageCapabilityConfigMapName},
						Data:       unchanged,
					},
					&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: nvcastorage.StorageCapabilityConfigMapName},
						Data:       map[string]string{nvcastorage.StorageCapabilityConfigMapKey: "same"},
					},
				)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			bc, backend := newConfigMapEventTestCache(t, ctx)

			err := tt.handle(ctx, bc)
			if tt.wantForce {
				require.ErrorContains(t, err, "version cannot be empty")
			} else {
				require.NoError(t, err)
			}

			stored, getErr := bc.clients.NVCAOP.NvcfV1().NVCFBackends(NVCAOperatorNamespace).
				Get(ctx, backend.Name, metav1.GetOptions{})
			require.NoError(t, getErr)
			assert.Equal(t, tt.wantForce,
				containsString(stored.Finalizers, cleanup.NVCAOperatorFinalizer))
		})
	}
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

type testResourceEventHandlerRegistration struct {
	synced bool
}

func (r testResourceEventHandlerRegistration) HasSynced() bool {
	return r.synced
}
