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

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
)

const testTransportRootCertPEM = `-----BEGIN CERTIFICATE-----
MIIDFzCCAf+gAwIBAgIUaNWvYBOx1GWnVct8jQamwHfprvMwDQYJKoZIhvcNAQEL
BQAwGzEZMBcGA1UEAwwQTlZDRiBUZXN0IFJvb3QgQTAeFw0yNjA0MzAyMTUyMjha
Fw0yNzA0MzAyMTUyMjhaMBsxGTAXBgNVBAMMEE5WQ0YgVGVzdCBSb290IEEwggEi
MA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQC7E+dEUss30Im2ixgsEXQZVdYA
lxz5ppRa5J1Olmbttb2upCjmdO/OxdkyP2Y1YA/pBrN4k98OGhxx9GJLVCtRL0ix
34tBAFDOn3RM2iM9oZvpbKIX+n1oUR8DXO6RiQ4Y4dLs3RLhfXrf6V9tL/YmYL7X
TLDaElPCcbrf6traGhNdOrwk9+GCtJP5CZsRePssPg9EmAxei2CerAYRtFHl8oEd
yTcK44LOR10Mo3wbz2axqWXjILG++l6o3Vw1SqN4x4GLBmeLNVE5Lkh8MOOsNbKj
rsi5dL5X6SI3J0/DkqjNRrbbNXLjt0lLOsq9ioIlf4aTzR+Ng9Uc2p/gsTfDAgMB
AAGjUzBRMB0GA1UdDgQWBBRZVSjXpLkvKxM2PAGRWiXYa8rSUzAfBgNVHSMEGDAW
gBRZVSjXpLkvKxM2PAGRWiXYa8rSUzAPBgNVHRMBAf8EBTADAQH/MA0GCSqGSIb3
DQEBCwUAA4IBAQCLv2LwHts5FnhafOWL/8lO5p5G0G8aL25lCL+RdqNoTGVIRRJV
f4RQQyGGbERYNaRdvNosh9u1aHzdGhi0i8oEW1N1TTyS6SmmP3/xMoJp3aL5E3AN
Ey9Naentws7yn+x4jxlyVqIecmH/LyiWpNNcKWXEGsDHJ9QQTNXicKiNwKNabKIv
RNOvPCpX1WFgj+rp2l3ahYACUzYbVGvuJXrF4fSawK0T/RWbXc7dkK68se0CGcuL
qswu4hFDV8na6EuT2ThxFXEuRb/OtIZdzfsTw0r9OixIlP1wGmzzQdZ9wi8mfe7i
LuQqFOQcWPEX70Ig+I6SWsb7VB6f0hZ2VGvA
-----END CERTIFICATE-----`

const testTransportRootFingerprint = "sha256:9a7814909424061a68756ee5c26aa1a1491b8d20a7b813fb24fa7e73b2fa1c93"

func TestCreatePodArtifactInstancesTransportTLSBundleInjectsOnlyLLMWorker(t *testing.T) {
	ctx := newTestContext()
	clients := makeMockKubeClients()
	kb := newTransportTLSTestBackend(clients, nvcaconfig.TransportTLSConfig{
		TrustMode:                nvcaconfig.TrustModeBundle,
		TrustBundleConfigMapName: "nvcf-transport-trust-bundle",
		TrustBundleKey:           "nvcf-ca-bundle.pem",
		TrustBundleFingerprint:   testTransportRootFingerprint,
		TrustBundlePEM:           testTransportRootCertPEM,
		InstallerImage:           "nvcr.io/nvidia/nvcf-byoc/nvca:test",
	})
	req := newTransportTLSTestRequest()

	instances, err := kb.CreatePodArtifactInstances(ctx, newTransportTLSTestPod(), req, transportTLSTestMutator)

	require.NoError(t, err)
	require.Len(t, instances, 1)
	cm, err := clients.K8s.CoreV1().ConfigMaps(RequestsNamespace).Get(ctx, "nvcf-transport-trust-bundle", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, testTransportRootCertPEM, cm.Data["nvcf-ca-bundle.pem"])
	assert.Equal(t, testTransportRootFingerprint, cm.Data["fingerprint"])

	createdPod, err := clients.K8s.CoreV1().Pods(RequestsNamespace).Get(ctx, getPodName("0-"+req.Name), metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []corev1.LocalObjectReference{{Name: "worker-image-pull-secret"}}, createdPod.Spec.ImagePullSecrets)
	assert.NotNil(t, findTransportTLSVolume(createdPod, "nvcf-transport-trust-bundle"))
	assert.NotNil(t, findTransportTLSVolume(createdPod, "nvcf-trust-merged-certs"))
	assert.NotNil(t, findTransportTLSInitContainer(createdPod, "nvcf-trust-bundle-install"))

	llmWorker := findTransportTLSContainer(createdPod, function.LLMWorkerContainerName)
	require.NotNil(t, llmWorker)
	assert.Equal(t, "/etc/ssl/certs/ca-certificates.crt",
		findTransportTLSEnvValue(llmWorker, "STARGATE_TLS_CERT_PATH"))
	assert.NotNil(t, findTransportTLSVolumeMount(llmWorker, "nvcf-trust-merged-certs"))

	for _, name := range []string{"inference", "smb-server"} {
		container := findTransportTLSContainer(createdPod, name)
		require.NotNil(t, container)
		assert.Empty(t, findTransportTLSEnvValue(container, "STARGATE_TLS_CERT_PATH"), name)
		assert.Nil(t, findTransportTLSVolumeMount(container, "nvcf-trust-merged-certs"), name)
	}
}

func TestCreatePodArtifactInstancesTransportTLSSystemDoesNotInjectBundle(t *testing.T) {
	ctx := newTestContext()
	clients := makeMockKubeClients()
	kb := newTransportTLSTestBackend(clients, nvcaconfig.TransportTLSConfig{
		TrustMode: nvcaconfig.TrustModeSystem,
	})
	req := newTransportTLSTestRequest()

	_, err := kb.CreatePodArtifactInstances(ctx, newTransportTLSTestPod(), req, transportTLSTestMutator)

	require.NoError(t, err)
	_, err = clients.K8s.CoreV1().ConfigMaps(RequestsNamespace).Get(ctx, "nvcf-transport-trust-bundle", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
	createdPod, err := clients.K8s.CoreV1().Pods(RequestsNamespace).Get(ctx, getPodName("0-"+req.Name), metav1.GetOptions{})
	require.NoError(t, err)
	assert.Nil(t, findTransportTLSVolume(createdPod, "nvcf-transport-trust-bundle"))
	assert.Nil(t, findTransportTLSInitContainer(createdPod, "nvcf-trust-bundle-install"))
	llmWorker := findTransportTLSContainer(createdPod, function.LLMWorkerContainerName)
	require.NotNil(t, llmWorker)
	assert.Empty(t, findTransportTLSEnvValue(llmWorker, "STARGATE_TLS_CERT_PATH"))
}

func TestCreatePodArtifactInstancesTransportTLSBundleRejectsMismatchedFingerprint(t *testing.T) {
	ctx := newTestContext()
	clients := makeMockKubeClients()
	kb := newTransportTLSTestBackend(clients, nvcaconfig.TransportTLSConfig{
		TrustMode:                nvcaconfig.TrustModeBundle,
		TrustBundleConfigMapName: "nvcf-transport-trust-bundle",
		TrustBundleKey:           "nvcf-ca-bundle.pem",
		TrustBundleFingerprint:   "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		TrustBundlePEM:           testTransportRootCertPEM,
	})

	_, err := kb.CreatePodArtifactInstances(ctx, newTransportTLSTestPod(), newTransportTLSTestRequest(),
		transportTLSTestMutator)

	require.Error(t, err)
	assert.True(t, nvcaerrors.IsTerminal(err))
	assert.Contains(t, err.Error(), "trustBundleFingerprint")
	pods, listErr := clients.K8s.CoreV1().Pods(RequestsNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, listErr)
	assert.Empty(t, pods.Items)
	_, getErr := clients.K8s.CoreV1().ConfigMaps(RequestsNamespace).Get(ctx, "nvcf-transport-trust-bundle",
		metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(getErr))
}

func TestCreatePodArtifactInstancesTransportTLSBundleRejectsInvalidConfigMapMetadata(t *testing.T) {
	tests := []struct {
		name      string
		mutateCfg func(*nvcaconfig.TransportTLSConfig)
		wantErr   string
	}{
		{
			name: "configmap name",
			mutateCfg: func(cfg *nvcaconfig.TransportTLSConfig) {
				cfg.TrustBundleConfigMapName = "Invalid_Name"
			},
			wantErr: "trustBundleConfigMapName",
		},
		{
			name: "configmap key",
			mutateCfg: func(cfg *nvcaconfig.TransportTLSConfig) {
				cfg.TrustBundleKey = "../ca.pem"
			},
			wantErr: "trustBundleKey",
		},
		{
			name: "reserved fingerprint key",
			mutateCfg: func(cfg *nvcaconfig.TransportTLSConfig) {
				cfg.TrustBundleKey = "fingerprint"
			},
			wantErr: "reserved key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			clients := makeMockKubeClients()
			cfg := nvcaconfig.TransportTLSConfig{
				TrustMode:                nvcaconfig.TrustModeBundle,
				TrustBundleConfigMapName: "nvcf-transport-trust-bundle",
				TrustBundleKey:           "nvcf-ca-bundle.pem",
				TrustBundleFingerprint:   testTransportRootFingerprint,
				TrustBundlePEM:           testTransportRootCertPEM,
				InstallerImage:           "nvcr.io/nvidia/nvcf-byoc/nvca:test",
			}
			tt.mutateCfg(&cfg)
			kb := newTransportTLSTestBackend(clients, cfg)

			_, err := kb.CreatePodArtifactInstances(ctx, newTransportTLSTestPod(), newTransportTLSTestRequest(),
				transportTLSTestMutator)

			require.Error(t, err)
			assert.True(t, nvcaerrors.IsTerminal(err))
			assert.Contains(t, err.Error(), tt.wantErr)
			pods, listErr := clients.K8s.CoreV1().Pods(RequestsNamespace).List(ctx, metav1.ListOptions{})
			require.NoError(t, listErr)
			assert.Empty(t, pods.Items)
		})
	}
}

func TestCreatePodArtifactInstancesTransportTLSBundleRequiresInstallerImageTerminal(t *testing.T) {
	ctx := newTestContext()
	clients := makeMockKubeClients()
	kb := newTransportTLSTestBackend(clients, nvcaconfig.TransportTLSConfig{
		TrustMode:                nvcaconfig.TrustModeBundle,
		TrustBundleConfigMapName: "nvcf-transport-trust-bundle",
		TrustBundleKey:           "nvcf-ca-bundle.pem",
		TrustBundleFingerprint:   testTransportRootFingerprint,
		TrustBundlePEM:           testTransportRootCertPEM,
	})

	_, err := kb.CreatePodArtifactInstances(ctx, newTransportTLSTestPod(), newTransportTLSTestRequest(),
		transportTLSTestMutator)

	require.Error(t, err)
	assert.True(t, nvcaerrors.IsTerminal(err))
	assert.Contains(t, err.Error(), "transportTls.installerImage")
	pods, listErr := clients.K8s.CoreV1().Pods(RequestsNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, listErr)
	assert.Empty(t, pods.Items)
}

func newTransportTLSTestBackend(clients *kubeclients.KubeClients, transportTLS nvcaconfig.TransportTLSConfig) K8sComputeBackend {
	return K8sComputeBackend{
		clients: clients,
		bk8s: &BackendK8sCache{
			podInstanceNamespace: RequestsNamespace,
			requestsNamespace:    RequestsNamespace,
			systemNamespace:      SystemNamespace,
			clients:              clients,
			featureFlagFetcher:   &featureflagmock.Fetcher{},
			eventRecorder:        record.NewFakeRecorder(10),
			k8sTimeConfig:        (&k8sutil.TimeConfig{}).Complete(),
			cfg: nvcaconfig.Config{
				Workload: nvcaconfig.WorkloadConfig{
					TransportTLS: &transportTLS,
				},
			},
		},
	}
}

func newTransportTLSTestRequest() *nvcav2beta1.ICMSRequest {
	return &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sr-transport-tls",
			Namespace: RequestsNamespace,
			UID:       k8stypes.UID("transport-tls-uid"),
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					InstanceCount: 1,
				},
			},
		},
	}
}

func newTransportTLSTestPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-template"},
		Spec: corev1.PodSpec{
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "worker-image-pull-secret"}},
			Containers: []corev1.Container{
				{Name: function.LLMWorkerContainerName, Image: "nvcr.io/nvcf/llm-worker:test"},
				{Name: "inference", Image: "nvcr.io/customer/inference:test"},
				{Name: "smb-server", Image: "nvcr.io/nvcf/smb-server:test"},
			},
		},
	}
}

func transportTLSTestMutator(obj client.Object) {
	obj.SetNamespace(RequestsNamespace)
}

func findTransportTLSContainer(pod *corev1.Pod, name string) *corev1.Container {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return &pod.Spec.Containers[i]
		}
	}
	return nil
}

func findTransportTLSInitContainer(pod *corev1.Pod, name string) *corev1.Container {
	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].Name == name {
			return &pod.Spec.InitContainers[i]
		}
	}
	return nil
}

func findTransportTLSVolume(pod *corev1.Pod, name string) *corev1.Volume {
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == name {
			return &pod.Spec.Volumes[i]
		}
	}
	return nil
}

func findTransportTLSVolumeMount(container *corev1.Container, name string) *corev1.VolumeMount {
	for i := range container.VolumeMounts {
		if container.VolumeMounts[i].Name == name {
			return &container.VolumeMounts[i]
		}
	}
	return nil
}

func findTransportTLSEnvValue(container *corev1.Container, name string) string {
	for _, env := range container.Env {
		if env.Name == name {
			return env.Value
		}
	}
	return ""
}
