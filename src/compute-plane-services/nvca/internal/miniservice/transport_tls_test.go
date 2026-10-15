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

package mscontroller

import (
	"context"
	"errors"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcav1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
)

const testTransportTLSRootCertPEM = `-----BEGIN CERTIFICATE-----
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

const testTransportTLSRootFingerprint = "sha256:9a7814909424061a68756ee5c26aa1a1491b8d20a7b813fb24fa7e73b2fa1c93"

func TestPrepareTransportTLSForWorkloadsInjectsPodLLMWorker(t *testing.T) {
	ctx := newTestContext()
	ms := &nvcav1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "llm-miniservice",
			UID:  k8stypes.UID("llm-miniservice-uid"),
		},
		Spec: nvcav1alpha1.MiniServiceSpec{Namespace: "worker-ns"},
	}
	crClient, _ := newFakeClient(mgrScheme, ms)
	r := &Reconciler{
		Client: crClient,
		cfg: nvcaconfig.Config{
			Workload: nvcaconfig.WorkloadConfig{
				TransportTLS: &nvcaconfig.TransportTLSConfig{
					TrustMode:                nvcaconfig.TrustModeBundle,
					TrustBundleConfigMapName: "nvcf-transport-trust-bundle",
					TrustBundleKey:           "nvcf-ca-bundle.pem",
					TrustBundleFingerprint:   testTransportTLSRootFingerprint,
					TrustBundlePEM:           testTransportTLSRootCertPEM,
					InstallerImage:           "nvcr.io/nvidia/nvcf-byoc/nvca:test",
				},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "llm-workload"},
		Spec: corev1.PodSpec{
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "worker-image-pull-secret"}},
			Containers: []corev1.Container{
				{Name: function.LLMWorkerContainerName, Image: "nvcr.io/nvcf/llm-worker:test"},
				{Name: "inference", Image: "nvcr.io/customer/inference:test"},
				{Name: "smb-server", Image: "nvcr.io/nvcf/smb-server:test"},
			},
		},
	}

	err := r.prepareTransportTLSForWorkloads(ctx, ms, []client.Object{pod})

	require.NoError(t, err)
	cm := &corev1.ConfigMap{}
	require.NoError(t, crClient.Get(ctx, client.ObjectKey{
		Namespace: "worker-ns",
		Name:      "nvcf-transport-trust-bundle",
	}, cm))
	assert.Equal(t, testTransportTLSRootCertPEM, cm.Data["nvcf-ca-bundle.pem"])
	assert.Equal(t, testTransportTLSRootFingerprint, cm.Data["fingerprint"])
	ownerRef := findTransportTLSOwnerRef(cm, ms)
	require.NotNil(t, ownerRef, "cluster-scoped MiniService owner reference should enable GC")
	assert.Nil(t, ownerRef.Controller, "trust ConfigMap can be shared by multiple MiniServices")
	assert.Nil(t, ownerRef.BlockOwnerDeletion)

	podSpec := pod.Spec
	assert.Equal(t, []corev1.LocalObjectReference{{Name: "worker-image-pull-secret"}}, podSpec.ImagePullSecrets)
	assert.NotNil(t, findWorkloadVolume(podSpec, "nvcf-transport-trust-bundle"))
	assert.NotNil(t, findWorkloadVolume(podSpec, "nvcf-trust-merged-certs"))
	assert.NotNil(t, findWorkloadInitContainer(podSpec, "nvcf-trust-bundle-install"))
	llmWorker := findWorkloadContainer(podSpec, function.LLMWorkerContainerName)
	require.NotNil(t, llmWorker)
	assert.Equal(t, "/etc/ssl/certs/ca-certificates.crt",
		findWorkloadEnvValue(llmWorker, "STARGATE_TLS_CERT_PATH"))
	assert.NotNil(t, findWorkloadVolumeMount(llmWorker, "nvcf-trust-merged-certs"))

	for _, name := range []string{"inference", "smb-server"} {
		container := findWorkloadContainer(podSpec, name)
		require.NotNil(t, container)
		assert.Empty(t, findWorkloadEnvValue(container, "STARGATE_TLS_CERT_PATH"), name)
		assert.Nil(t, findWorkloadVolumeMount(container, "nvcf-trust-merged-certs"), name)
	}
}

func TestPrepareTransportTLSForWorkloadsKeepsOwnerRefForSameNamespaceConfigMap(t *testing.T) {
	ctx := newTestContext()
	ms := &nvcav1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "llm-miniservice",
			Namespace: "worker-ns",
			UID:       k8stypes.UID("llm-miniservice-uid"),
		},
		Spec: nvcav1alpha1.MiniServiceSpec{Namespace: "worker-ns"},
	}
	crClient, _ := newFakeClient(mgrScheme, ms)
	r := newTransportTLSReconciler(crClient, nvcaconfig.TransportTLSConfig{
		TrustMode:                nvcaconfig.TrustModeBundle,
		TrustBundleConfigMapName: "nvcf-transport-trust-bundle",
		TrustBundleKey:           "nvcf-ca-bundle.pem",
		TrustBundleFingerprint:   testTransportTLSRootFingerprint,
		TrustBundlePEM:           testTransportTLSRootCertPEM,
		InstallerImage:           "nvcr.io/nvidia/nvcf-byoc/nvca:test",
	})

	err := r.prepareTransportTLSForWorkloads(ctx, ms, []client.Object{newTransportTLSPod()})

	require.NoError(t, err)
	cm := &corev1.ConfigMap{}
	require.NoError(t, crClient.Get(ctx, client.ObjectKey{
		Namespace: "worker-ns",
		Name:      "nvcf-transport-trust-bundle",
	}, cm))
	assert.True(t, hasTransportTLSOwnerRef(cm, ms), "same-namespace owner reference should still enable GC")
}

func TestPrepareTransportTLSForWorkloadsNormalizesExistingControllerOwnerRef(t *testing.T) {
	ctx := newTestContext()
	ms := &nvcav1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "llm-miniservice",
			UID:  k8stypes.UID("llm-miniservice-uid"),
		},
		Spec: nvcav1alpha1.MiniServiceSpec{Namespace: "worker-ns"},
	}
	otherMS := &nvcav1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other-miniservice",
			UID:  k8stypes.UID("other-miniservice-uid"),
		},
	}
	oldOwnerRef := transportTLSOwnerReference(ms)
	oldOwnerRef.Controller = ptr.To(true)
	oldOwnerRef.BlockOwnerDeletion = ptr.To(true)
	otherOldOwnerRef := transportTLSOwnerReference(otherMS)
	otherOldOwnerRef.Controller = ptr.To(true)
	otherOldOwnerRef.BlockOwnerDeletion = ptr.To(true)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "nvcf-transport-trust-bundle",
			Namespace:       "worker-ns",
			OwnerReferences: []metav1.OwnerReference{oldOwnerRef, otherOldOwnerRef},
		},
	}
	crClient, _ := newFakeClient(mgrScheme, ms, cm)
	r := newTransportTLSReconciler(crClient, nvcaconfig.TransportTLSConfig{
		TrustMode:                nvcaconfig.TrustModeBundle,
		TrustBundleConfigMapName: "nvcf-transport-trust-bundle",
		TrustBundleKey:           "nvcf-ca-bundle.pem",
		TrustBundleFingerprint:   testTransportTLSRootFingerprint,
		TrustBundlePEM:           testTransportTLSRootCertPEM,
		InstallerImage:           "nvcr.io/nvidia/nvcf-byoc/nvca:test",
	})

	err := r.prepareTransportTLSForWorkloads(ctx, ms, []client.Object{newTransportTLSPod()})

	require.NoError(t, err)
	require.NoError(t, crClient.Get(ctx, client.ObjectKey{
		Namespace: "worker-ns",
		Name:      "nvcf-transport-trust-bundle",
	}, cm))
	ownerRef := findTransportTLSOwnerRef(cm, ms)
	require.NotNil(t, ownerRef)
	assert.Nil(t, ownerRef.Controller)
	assert.Nil(t, ownerRef.BlockOwnerDeletion)
	otherOwnerRef := findTransportTLSOwnerRef(cm, otherMS)
	require.NotNil(t, otherOwnerRef)
	assert.Nil(t, otherOwnerRef.Controller)
	assert.Nil(t, otherOwnerRef.BlockOwnerDeletion)
}

func TestPrepareTransportTLSForWorkloadsDropsCrossNamespaceOwnerRefFromExistingConfigMap(t *testing.T) {
	ctx := newTestContext()
	ms := &nvcav1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "llm-miniservice",
			Namespace: "nvca-system",
			UID:       k8stypes.UID("llm-miniservice-uid"),
		},
		Spec: nvcav1alpha1.MiniServiceSpec{Namespace: "worker-ns"},
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "nvcf-transport-trust-bundle",
			Namespace:       "worker-ns",
			OwnerReferences: []metav1.OwnerReference{transportTLSOwnerReference(ms)},
		},
	}
	crClient, _ := newFakeClient(mgrScheme, ms, cm)
	r := newTransportTLSReconciler(crClient, nvcaconfig.TransportTLSConfig{
		TrustMode:                nvcaconfig.TrustModeBundle,
		TrustBundleConfigMapName: "nvcf-transport-trust-bundle",
		TrustBundleKey:           "nvcf-ca-bundle.pem",
		TrustBundleFingerprint:   testTransportTLSRootFingerprint,
		TrustBundlePEM:           testTransportTLSRootCertPEM,
		InstallerImage:           "nvcr.io/nvidia/nvcf-byoc/nvca:test",
	})

	err := r.prepareTransportTLSForWorkloads(ctx, ms, []client.Object{newTransportTLSPod()})

	require.NoError(t, err)
	require.NoError(t, crClient.Get(ctx, client.ObjectKey{
		Namespace: "worker-ns",
		Name:      "nvcf-transport-trust-bundle",
	}, cm))
	assert.False(t, hasTransportTLSOwnerRef(cm, ms), "cross-namespace owner reference should be cleaned up on update")
}

func TestPrepareTransportTLSForWorkloadsReturnsTerminalErrorForInvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		mutateCfg func(*nvcaconfig.TransportTLSConfig)
	}{
		{
			name: "mismatched fingerprint",
			mutateCfg: func(cfg *nvcaconfig.TransportTLSConfig) {
				cfg.TrustBundleFingerprint = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
			},
		},
		{
			name: "invalid ConfigMap name",
			mutateCfg: func(cfg *nvcaconfig.TransportTLSConfig) {
				cfg.TrustBundleConfigMapName = "Invalid_Name"
			},
		},
		{
			name: "invalid ConfigMap key",
			mutateCfg: func(cfg *nvcaconfig.TransportTLSConfig) {
				cfg.TrustBundleKey = "../ca.pem"
			},
		},
		{
			name: "reserved fingerprint key",
			mutateCfg: func(cfg *nvcaconfig.TransportTLSConfig) {
				cfg.TrustBundleKey = "fingerprint"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			ms := &nvcav1alpha1.MiniService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "llm-miniservice",
					Namespace: "worker-ns",
					UID:       k8stypes.UID("llm-miniservice-uid"),
				},
				Spec: nvcav1alpha1.MiniServiceSpec{Namespace: "worker-ns"},
			}
			cfg := nvcaconfig.TransportTLSConfig{
				TrustMode:                nvcaconfig.TrustModeBundle,
				TrustBundleConfigMapName: "nvcf-transport-trust-bundle",
				TrustBundleKey:           "nvcf-ca-bundle.pem",
				TrustBundleFingerprint:   testTransportTLSRootFingerprint,
				TrustBundlePEM:           testTransportTLSRootCertPEM,
				InstallerImage:           "nvcr.io/nvidia/nvcf-byoc/nvca:test",
			}
			tt.mutateCfg(&cfg)
			crClient, _ := newFakeClient(mgrScheme, ms)
			r := newTransportTLSReconciler(crClient, cfg)

			err := r.prepareTransportTLSForWorkloads(ctx, ms, []client.Object{newTransportTLSPod()})

			require.Error(t, err)
			assert.True(t, errors.Is(err, reconcile.TerminalError(nil)), "invalid static transport TLS config should fail terminally")
		})
	}
}

func TestPrepareTransportTLSForWorkloadsKeepsConfigMapAPIErrorsRetryable(t *testing.T) {
	transportTLSKey := client.ObjectKey{Namespace: "worker-ns", Name: "nvcf-transport-trust-bundle"}
	existingConfigMap := func() *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      transportTLSKey.Name,
				Namespace: transportTLSKey.Namespace,
			},
			Data: map[string]string{
				"nvcf-ca-bundle.pem": "old-bundle",
				"fingerprint":        "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			},
		}
	}

	tests := []struct {
		name       string
		intercepts func(error) interceptor.Funcs
		existing   *corev1.ConfigMap
	}{
		{
			name: "get",
			intercepts: func(injectedErr error) interceptor.Funcs {
				return interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if key == transportTLSKey {
							if _, ok := obj.(*corev1.ConfigMap); ok {
								return injectedErr
							}
						}
						return c.Get(ctx, key, obj, opts...)
					},
				}
			},
		},
		{
			name: "create",
			intercepts: func(injectedErr error) interceptor.Funcs {
				return interceptor.Funcs{
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						if _, ok := obj.(*corev1.ConfigMap); ok {
							return injectedErr
						}
						return c.Create(ctx, obj, opts...)
					},
				}
			},
		},
		{
			name:     "update",
			existing: existingConfigMap(),
			intercepts: func(injectedErr error) interceptor.Funcs {
				return interceptor.Funcs{
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						if _, ok := obj.(*corev1.ConfigMap); ok {
							return injectedErr
						}
						return c.Update(ctx, obj, opts...)
					},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			ms := &nvcav1alpha1.MiniService{
				ObjectMeta: metav1.ObjectMeta{
					Name: "llm-miniservice",
					UID:  k8stypes.UID("llm-miniservice-uid"),
				},
				Spec: nvcav1alpha1.MiniServiceSpec{Namespace: "worker-ns"},
			}
			injectedErr := apierrors.NewTooManyRequests("simulated apiserver failure", 1)
			objs := []client.Object{ms}
			if tt.existing != nil {
				objs = append(objs, tt.existing)
			}
			crClient, _ := newFakeClientWithInterceptors(mgrScheme, tt.intercepts(injectedErr), objs...)
			r := newTransportTLSReconciler(crClient, nvcaconfig.TransportTLSConfig{
				TrustMode:                nvcaconfig.TrustModeBundle,
				TrustBundleConfigMapName: "nvcf-transport-trust-bundle",
				TrustBundleKey:           "nvcf-ca-bundle.pem",
				TrustBundleFingerprint:   testTransportTLSRootFingerprint,
				TrustBundlePEM:           testTransportTLSRootCertPEM,
				InstallerImage:           "nvcr.io/nvidia/nvcf-byoc/nvca:test",
			})

			err := r.prepareTransportTLSForWorkloads(ctx, ms, []client.Object{newTransportTLSPod()})

			require.ErrorIs(t, err, injectedErr)
			assert.False(t, errors.Is(err, reconcile.TerminalError(nil)), "Kubernetes API failures should be retried")
		})
	}
}

func newTransportTLSReconciler(crClient client.Client, cfg nvcaconfig.TransportTLSConfig) *Reconciler {
	return &Reconciler{
		Client: crClient,
		cfg: nvcaconfig.Config{
			Workload: nvcaconfig.WorkloadConfig{
				TransportTLS: &cfg,
			},
		},
	}
}

func newTransportTLSPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "llm-workload"},
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

func hasTransportTLSOwnerRef(cm *corev1.ConfigMap, ms *nvcav1alpha1.MiniService) bool {
	return findTransportTLSOwnerRef(cm, ms) != nil
}

func findTransportTLSOwnerRef(cm *corev1.ConfigMap, ms *nvcav1alpha1.MiniService) *metav1.OwnerReference {
	for _, owner := range cm.OwnerReferences {
		if owner.Name == ms.Name && owner.UID == ms.UID {
			return &owner
		}
	}
	return nil
}

func findWorkloadContainer(podSpec corev1.PodSpec, name string) *corev1.Container {
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == name {
			return &podSpec.Containers[i]
		}
	}
	return nil
}

func findWorkloadInitContainer(podSpec corev1.PodSpec, name string) *corev1.Container {
	for i := range podSpec.InitContainers {
		if podSpec.InitContainers[i].Name == name {
			return &podSpec.InitContainers[i]
		}
	}
	return nil
}

func findWorkloadVolume(podSpec corev1.PodSpec, name string) *corev1.Volume {
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == name {
			return &podSpec.Volumes[i]
		}
	}
	return nil
}

func findWorkloadVolumeMount(container *corev1.Container, name string) *corev1.VolumeMount {
	for i := range container.VolumeMounts {
		if container.VolumeMounts[i].Name == name {
			return &container.VolumeMounts[i]
		}
	}
	return nil
}

func findWorkloadEnvValue(container *corev1.Container, name string) string {
	for _, env := range container.Env {
		if env.Name == name {
			return env.Value
		}
	}
	return ""
}
