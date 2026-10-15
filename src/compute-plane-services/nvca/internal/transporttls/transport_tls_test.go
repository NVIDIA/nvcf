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

package transporttls

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

const testRootCertPEM = `-----BEGIN CERTIFICATE-----
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

const testRootFingerprint = "sha256:9a7814909424061a68756ee5c26aa1a1491b8d20a7b813fb24fa7e73b2fa1c93"

func TestFingerprintTrustBundleMatchesControlPlaneProfileVector(t *testing.T) {
	got, err := FingerprintTrustBundle(testRootCertPEM)

	require.NoError(t, err)
	assert.Equal(t, testRootFingerprint, got)
}

func TestValidateConfigRejectsMismatchedFingerprint(t *testing.T) {
	cfg := NormalizeConfig(nvcaconfig.TransportTLSConfig{
		TrustMode:              nvcaconfig.TrustModeBundle,
		TrustBundleFingerprint: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		TrustBundlePEM:         testRootCertPEM,
	})

	err := ValidateConfig(cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "trustBundleFingerprint")
}

func TestValidateConfigRejectsInvalidKubernetesNames(t *testing.T) {
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
				cfg.TrustBundleKey = TrustBundleFingerprintKey
			},
			wantErr: "reserved key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NormalizeConfig(nvcaconfig.TransportTLSConfig{
				TrustMode:              nvcaconfig.TrustModeBundle,
				TrustBundleFingerprint: testRootFingerprint,
				TrustBundlePEM:         testRootCertPEM,
			})
			tt.mutateCfg(&cfg)

			err := ValidateConfig(cfg)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestInjectIntoPodSpecOnlyMutatesLLMWorker(t *testing.T) {
	podSpec := &corev1.PodSpec{
		Containers: []corev1.Container{
			{Name: function.LLMWorkerContainerName, Image: "nvcr.io/nvcf/llm-worker:test"},
			{Name: "inference", Image: "nvcr.io/customer/inference:test"},
			{Name: "smb-server", Image: "nvcr.io/nvcf/smb-server:test"},
		},
	}

	err := InjectIntoPodSpec(podSpec, NormalizeConfig(nvcaconfig.TransportTLSConfig{
		TrustMode:              nvcaconfig.TrustModeBundle,
		TrustBundleFingerprint: testRootFingerprint,
		TrustBundlePEM:         testRootCertPEM,
		InstallerImage:         "nvcr.io/nvidia/nvcf-byoc/nvca:test",
	}))
	require.NoError(t, err)

	trustBundleVolume := findTestVolume(podSpec, TrustBundleVolumeName)
	require.NotNil(t, trustBundleVolume)
	require.NotNil(t, trustBundleVolume.ConfigMap)
	require.NotNil(t, trustBundleVolume.ConfigMap.Optional)
	assert.False(t, *trustBundleVolume.ConfigMap.Optional)
	assert.NotNil(t, findTestVolume(podSpec, MergedCertsVolumeName))
	assert.NotNil(t, findTestInitContainer(podSpec, InstallContainerName))

	llmWorker := findTestContainer(podSpec, function.LLMWorkerContainerName)
	require.NotNil(t, llmWorker)
	assert.Equal(t, SystemCertFile, findTestEnvValue(llmWorker, CertPathEnv))
	assert.NotNil(t, findTestVolumeMount(llmWorker, MergedCertsVolumeName))

	for _, name := range []string{"inference", "smb-server"} {
		container := findTestContainer(podSpec, name)
		require.NotNil(t, container)
		assert.Empty(t, findTestEnvValue(container, CertPathEnv), name)
		assert.Nil(t, findTestVolumeMount(container, MergedCertsVolumeName), name)
	}
}

func TestInjectIntoPodSpecUsesNVCAInstallCommand(t *testing.T) {
	podSpec := &corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  function.LLMWorkerContainerName,
			Image: "nvcr.io/nvcf/llm-worker:test",
		}},
	}

	err := InjectIntoPodSpec(podSpec, NormalizeConfig(nvcaconfig.TransportTLSConfig{
		TrustMode:              nvcaconfig.TrustModeBundle,
		TrustBundleFingerprint: testRootFingerprint,
		TrustBundlePEM:         testRootCertPEM,
		InstallerImage:         "nvcr.io/nvidia/nvcf-byoc/nvca:test",
	}))
	require.NoError(t, err)

	installContainer := findTestInitContainer(podSpec, InstallContainerName)
	require.NotNil(t, installContainer)
	assert.Equal(t, "nvcr.io/nvidia/nvcf-byoc/nvca:test", installContainer.Image)
	assert.Equal(t, corev1.PullIfNotPresent, installContainer.ImagePullPolicy)
	assert.Equal(t, []string{InstallCommandPath}, installContainer.Command)
	assert.Equal(t, []string{
		"--system-bundle", SystemCertFile,
		"--trust-bundle", TrustBundleMountPath + "/" + DefaultTrustBundleKey,
		"--output-bundle", MergedCertsFile,
		"--expected-fingerprint", testRootFingerprint,
	}, installContainer.Args)
	require.NotNil(t, installContainer.SecurityContext)
	require.NotNil(t, installContainer.SecurityContext.RunAsUser)
	assert.Equal(t, int64(0), *installContainer.SecurityContext.RunAsUser)
	require.NotNil(t, installContainer.SecurityContext.RunAsNonRoot)
	assert.False(t, *installContainer.SecurityContext.RunAsNonRoot)
}

func TestInjectIntoPodSpecUsesConfiguredInstallerImageWhenLegacyEnvIsSet(t *testing.T) {
	t.Setenv("NVCF_TRUST_BUNDLE_INSTALLER_IMAGE", "nvcr.io/private-mirror/nvca:test")
	podSpec := &corev1.PodSpec{
		InitContainers: []corev1.Container{{
			Name:            InstallContainerName,
			Image:           "nvcr.io/legacy/installer:42",
			ImagePullPolicy: corev1.PullAlways,
		}},
		Containers: []corev1.Container{{
			Name:  function.LLMWorkerContainerName,
			Image: "nvcr.io/nvcf/llm-worker:test",
		}},
	}

	err := InjectIntoPodSpec(podSpec, NormalizeConfig(nvcaconfig.TransportTLSConfig{
		TrustMode:              nvcaconfig.TrustModeBundle,
		TrustBundleFingerprint: testRootFingerprint,
		TrustBundlePEM:         testRootCertPEM,
		InstallerImage:         "nvcr.io/nvidia/nvcf-byoc/nvca:test",
	}))
	require.NoError(t, err)

	installContainer := findTestInitContainer(podSpec, InstallContainerName)
	require.NotNil(t, installContainer)
	assert.Equal(t, "nvcr.io/nvidia/nvcf-byoc/nvca:test", installContainer.Image)
	assert.Equal(t, corev1.PullIfNotPresent, installContainer.ImagePullPolicy)
}

func TestInjectIntoPodSpecRejectsBundleModeWithoutInstallerImage(t *testing.T) {
	podSpec := &corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  function.LLMWorkerContainerName,
			Image: "nvcr.io/nvcf/llm-worker:test",
		}},
	}

	err := InjectIntoPodSpec(podSpec, NormalizeConfig(nvcaconfig.TransportTLSConfig{
		TrustMode:              nvcaconfig.TrustModeBundle,
		TrustBundleFingerprint: testRootFingerprint,
		TrustBundlePEM:         testRootCertPEM,
	}))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "transportTls.installerImage")
}

func findTestContainer(podSpec *corev1.PodSpec, name string) *corev1.Container {
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == name {
			return &podSpec.Containers[i]
		}
	}
	return nil
}

func findTestInitContainer(podSpec *corev1.PodSpec, name string) *corev1.Container {
	for i := range podSpec.InitContainers {
		if podSpec.InitContainers[i].Name == name {
			return &podSpec.InitContainers[i]
		}
	}
	return nil
}

func findTestVolume(podSpec *corev1.PodSpec, name string) *corev1.Volume {
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == name {
			return &podSpec.Volumes[i]
		}
	}
	return nil
}

func findTestVolumeMount(container *corev1.Container, name string) *corev1.VolumeMount {
	for i := range container.VolumeMounts {
		if container.VolumeMounts[i].Name == name {
			return &container.VolumeMounts[i]
		}
	}
	return nil
}

func findTestEnvValue(container *corev1.Container, name string) string {
	for _, env := range container.Env {
		if env.Name == name {
			return env.Value
		}
	}
	return ""
}
