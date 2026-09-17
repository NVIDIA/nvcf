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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/reconcile/clustermgmt"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestGetSystemNamespace(t *testing.T) {
	tests := []struct {
		name     string
		nb       *nvidiaiov1.NVCFBackend
		expected string
	}{
		{
			name: "use custom system namespace",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							SystemNamespace: "custom-system-ns",
						},
					},
				},
			},
			expected: "custom-system-ns",
		},
		{
			name: "use default when not specified",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{},
					},
				},
			},
			expected: DefaultNVCASystemNamespace,
		},
		{
			name: "use default when empty string",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							SystemNamespace: "",
						},
					},
				},
			},
			expected: DefaultNVCASystemNamespace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getSystemNamespace(tt.nb)
			if result != tt.expected {
				t.Errorf("getSystemNamespace() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetRequestsNamespace(t *testing.T) {
	tests := []struct {
		name     string
		nb       *nvidiaiov1.NVCFBackend
		expected string
	}{
		{
			name: "use custom requests namespace",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							RequestsNamespace: "custom-requests-ns",
						},
					},
				},
			},
			expected: "custom-requests-ns",
		},
		{
			name: "use default when not specified",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{},
					},
				},
			},
			expected: DefaultNVCARequestsNamespace,
		},
		{
			name: "use default when empty string",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							RequestsNamespace: "",
						},
					},
				},
			},
			expected: DefaultNVCARequestsNamespace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getRequestsNamespace(tt.nb)
			if result != tt.expected {
				t.Errorf("getRequestsNamespace() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetBackendType(t *testing.T) {
	tests := []struct {
		name     string
		nb       *nvidiaiov1.NVCFBackend
		expected string
	}{
		{
			name: "use custom backend type",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							BackendType: "BCP",
						},
					},
				},
			},
			expected: "BCP",
		},
		{
			name: "use default when not specified",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{},
					},
				},
			},
			expected: DefaultBackendTypeK8s,
		},
		{
			name: "use default when empty string",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							BackendType: "",
						},
					},
				},
			},
			expected: DefaultBackendTypeK8s,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getBackendType(tt.nb)
			if result != tt.expected {
				t.Errorf("getBackendType() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetClusterLogLevel(t *testing.T) {
	tests := []struct {
		name     string
		nb       *nvidiaiov1.NVCFBackend
		expected string
	}{
		{
			name: "use custom log level",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							LogLevel: "debug",
						},
					},
				},
			},
			expected: "debug",
		},
		{
			name: "use default when not specified",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{},
					},
				},
			},
			expected: DefaultLogLevel,
		},
		{
			name: "use default when empty string",
			nb: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							LogLevel: "",
						},
					},
				},
			},
			expected: DefaultLogLevel,
		},
		{
			name: "preserve case sensitivity",
			nb: &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: "test-ns",
				},
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterConfig: nvidiaiov1.ClusterConfig{
							LogLevel: "INFO",
						},
					},
				},
			},
			expected: "INFO",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getClusterLogLevel(tt.nb)
			if result != tt.expected {
				t.Errorf("getClusterLogLevel() = %v, want %v", result, tt.expected)
			}
		})
	}
}

const (
	externalLabelKey      = "platform.example.com/owner"
	externalAnnotationKey = "platform.example.com/audit"
)

func newNamespaceCache(t *testing.T, objects ...runtime.Object) (*BackendK8sCache, *fake.Clientset) {
	t.Helper()

	clientset := fake.NewSimpleClientset(objects...)
	return &BackendK8sCache{
		clients: &kubeclients.KubeClients{K8s: clientset},
	}, clientset
}

func TestBackendK8sCache_CreateOrUpdateNamespaceMetadataOwnership(t *testing.T) {
	ctx := context.Background()

	t.Run("external labels and annotations survive reconciliation", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "test-ns",
				Labels:      map[string]string{externalLabelKey: "platform", InstanceLabelKey: "stale"},
				Annotations: map[string]string{externalAnnotationKey: "enabled"},
			},
		})

		desired := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "test-ns",
				Labels:      getAppLabels(),
				Annotations: map[string]string{ClusterName: "test-cluster"},
			},
		}

		require.NoError(t, bc.createOrUpdateNamespace(ctx, desired))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, "test-ns", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "platform", got.Labels[externalLabelKey])
		assert.Equal(t, "enabled", got.Annotations[externalAnnotationKey])
		assert.Equal(t, nvcaoptypes.NVCAModuleName, got.Labels[InstanceLabelKey])
		assert.Equal(t, "test-cluster", got.Annotations[ClusterName])
	})

	t.Run("namespace identity and other metadata survive reconciliation", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "test-ns",
				UID:             "namespace-uid",
				Finalizers:      []string{"example.com/protect"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "owner"}},
			},
			Spec: corev1.NamespaceSpec{Finalizers: []corev1.FinalizerName{corev1.FinalizerKubernetes}},
		})

		desired := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-ns", Labels: getAppLabels()},
		}

		require.NoError(t, bc.createOrUpdateNamespace(ctx, desired))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, "test-ns", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, types.UID("namespace-uid"), got.UID)
		assert.Equal(t, []string{"example.com/protect"}, got.Finalizers)
		require.Len(t, got.OwnerReferences, 1)
		assert.Equal(t, "owner", got.OwnerReferences[0].Name)
		assert.Equal(t, []corev1.FinalizerName{corev1.FinalizerKubernetes}, got.Spec.Finalizers)
	})

	t.Run("optional owned key is removed when the desired object omits it", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-ns",
				Labels: map[string]string{
					clustermgmt.ShaderCacheLabelKey: "true",
					externalLabelKey:                "platform",
				},
			},
		})

		desired := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-ns", Labels: getAppLabels()},
		}

		require.NoError(t, bc.createOrUpdateNamespace(ctx, desired, clustermgmt.ShaderCacheLabelKey))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, "test-ns", metav1.GetOptions{})
		require.NoError(t, err)
		assert.NotContains(t, got.Labels, clustermgmt.ShaderCacheLabelKey)
		assert.Equal(t, "platform", got.Labels[externalLabelKey])
	})

	t.Run("optional owned key is kept when the desired object sets it", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-ns"},
		})

		desired := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "test-ns",
				Labels: map[string]string{clustermgmt.ShaderCacheLabelKey: "true"},
			},
		}

		require.NoError(t, bc.createOrUpdateNamespace(ctx, desired, clustermgmt.ShaderCacheLabelKey))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, "test-ns", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "true", got.Labels[clustermgmt.ShaderCacheLabelKey])
	})

	t.Run("nil metadata maps on either side are handled", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-ns"},
		})

		require.NoError(t, bc.createOrUpdateNamespace(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-ns", Labels: getAppLabels()},
		}))
		require.NoError(t, bc.createOrUpdateNamespace(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-ns"},
		}))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, "test-ns", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, nvcaoptypes.NVCAModuleName, got.Labels[InstanceLabelKey])
		assert.Nil(t, got.Annotations)
	})

	t.Run("missing namespace is created with the desired metadata", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t)

		desired := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "test-ns",
				Labels:      getAppLabels(),
				Annotations: map[string]string{ClusterName: "test-cluster"},
			},
		}

		require.NoError(t, bc.createOrUpdateNamespace(ctx, desired))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, "test-ns", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, nvcaoptypes.NVCAModuleName, got.Labels[InstanceLabelKey])
		assert.Equal(t, "test-cluster", got.Annotations[ClusterName])
	})

	t.Run("no write is issued when the owned metadata already matches", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "test-ns",
				Labels:      getAppLabels(),
				Annotations: map[string]string{externalAnnotationKey: "enabled"},
			},
		})
		clientset.ClearActions()

		require.NoError(t, bc.createOrUpdateNamespace(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-ns", Labels: getAppLabels()},
		}))

		for _, action := range clientset.Actions() {
			assert.NotEqual(t, "update", action.GetVerb(), "namespace was rewritten with no metadata change")
		}
	})

	t.Run("concurrent external metadata change is merged on conflict", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-ns"},
		})

		nsResource := corev1.SchemeGroupVersion.WithResource("namespaces")
		conflicts := 0
		clientset.PrependReactor("update", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if conflicts > 0 {
				return false, nil, nil
			}
			conflicts++

			// Simulate another controller writing the namespace between our read and our update.
			external, err := clientset.Tracker().Get(nsResource, "", "test-ns")
			if err != nil {
				return true, nil, err
			}
			externalNS, ok := external.(*corev1.Namespace)
			if !ok {
				return true, nil, fmt.Errorf("unexpected object type %T", external)
			}
			externalNS.Annotations = map[string]string{externalAnnotationKey: "enabled"}
			if err := clientset.Tracker().Update(nsResource, externalNS, ""); err != nil {
				return true, nil, err
			}

			return true, nil, k8serrors.NewConflict(
				corev1.Resource("namespaces"), "test-ns", fmt.Errorf("object was modified"))
		})

		require.NoError(t, bc.createOrUpdateNamespace(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-ns", Labels: getAppLabels()},
		}))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, "test-ns", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, 1, conflicts)
		assert.Equal(t, "enabled", got.Annotations[externalAnnotationKey])
		assert.Equal(t, nvcaoptypes.NVCAModuleName, got.Labels[InstanceLabelKey])
	})
}

func TestSetupNamespacesPreserveExternalMetadata(t *testing.T) {
	ctx := context.Background()

	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "test-cluster",
					ClusterGroupName: "test-group",
				},
			},
		},
	}

	externalMetadata := func(name string) *corev1.Namespace {
		return &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Labels:      map[string]string{externalLabelKey: "platform"},
				Annotations: map[string]string{externalAnnotationKey: "enabled"},
			},
		}
	}

	t.Run("system namespace", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t, externalMetadata(getSystemNamespace(nb)))

		require.NoError(t, bc.setupSystemNamespace(ctx, nb))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "platform", got.Labels[externalLabelKey])
		assert.Equal(t, "enabled", got.Annotations[externalAnnotationKey])
		assert.Equal(t, nvcaoptypes.NVCAModuleName, got.Labels[InstanceLabelKey])
		assert.Equal(t, "test-cluster", got.Annotations[ClusterName])
		assert.Equal(t, "test-group", got.Annotations[ClusterGroupKey])
	})

	t.Run("requests namespace with gxcache enabled", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t, externalMetadata(getRequestsNamespace(nb)))
		bc.enableGXCache = true

		require.NoError(t, bc.setupRequestsNamespace(ctx, nb))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "platform", got.Labels[externalLabelKey])
		assert.Equal(t, "enabled", got.Annotations[externalAnnotationKey])
		assert.Equal(t, nvcaoptypes.NVCAModuleName, got.Labels[ManagedbyLabelKey])
		assert.Equal(t, WorkloadInstanceTypeValuePodSpec, got.Labels[nvcatypes.WorkloadInstanceTypeLabel])
		assert.Equal(t, "true", got.Labels[clustermgmt.ShaderCacheLabelKey])
	})

	t.Run("requests namespace drops the gxcache label when disabled", func(t *testing.T) {
		existing := externalMetadata(getRequestsNamespace(nb))
		existing.Labels[clustermgmt.ShaderCacheLabelKey] = "true"
		bc, clientset := newNamespaceCache(t, existing)

		require.NoError(t, bc.setupRequestsNamespace(ctx, nb))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.NoError(t, err)
		assert.NotContains(t, got.Labels, clustermgmt.ShaderCacheLabelKey)
		assert.Equal(t, "platform", got.Labels[externalLabelKey])
		assert.Equal(t, "enabled", got.Annotations[externalAnnotationKey])
	})
}
