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
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
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

func TestEffectiveRequestsNamespace(t *testing.T) {
	withBackendNamespace := func(ns string) *nvidiaiov1.NVCFBackend {
		return &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
			ClusterConfig: nvidiaiov1.ClusterConfig{RequestsNamespace: ns},
		}}}
	}
	withOverride := func(ns string) nvcaconfig.Config {
		var cfg nvcaconfig.Config
		cfg.Agent.RequestsNamespace = ns
		return cfg
	}

	tests := []struct {
		name     string
		nb       *nvidiaiov1.NVCFBackend
		mergeCfg nvcaconfig.Config
		expected string
	}{
		{
			name:     "default when neither source sets it",
			nb:       withBackendNamespace(""),
			expected: DefaultNVCARequestsNamespace,
		},
		{
			name:     "backend value when there is no override",
			nb:       withBackendNamespace("backend-ns"),
			expected: "backend-ns",
		},
		{
			name:     "override wins over the default",
			nb:       withBackendNamespace(""),
			mergeCfg: withOverride("team-x"),
			expected: "team-x",
		},
		{
			name:     "override wins over the backend value",
			nb:       withBackendNamespace("backend-ns"),
			mergeCfg: withOverride("team-x"),
			expected: "team-x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, effectiveRequestsNamespace(tt.nb, tt.mergeCfg))
		})
	}
}

func TestGetEffectiveRequestsNamespaceReadsMergeConfig(t *testing.T) {
	ctx := context.Background()
	nb := &nvidiaiov1.NVCFBackend{}
	bc, clientset := newNamespaceCache(t)
	bc.operatorNamespace = NVCAOperatorNamespace

	got, err := bc.getEffectiveRequestsNamespace(ctx, nb)
	require.NoError(t, err)
	assert.Equal(t, DefaultNVCARequestsNamespace, got)

	_, err = clientset.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigMergeConfigMapName, Namespace: NVCAOperatorNamespace},
		Data:       map[string]string{agentConfigFile: "agent:\n  requestsNamespace: team-x\n"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	got, err = bc.getEffectiveRequestsNamespace(ctx, nb)
	require.NoError(t, err)
	assert.Equal(t, "team-x", got)
}

func TestValidateRequestsNamespaceConfig(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		wantErr   string
	}{
		{name: "unset", namespace: ""},
		{name: "valid name", namespace: "team-x"},
		{name: "uppercase and underscore", namespace: "Team_X", wantErr: "is not a valid namespace name"},
		{name: "leading dash", namespace: "-team", wantErr: "is not a valid namespace name"},
		{name: "longer than 63 characters", namespace: fmt.Sprintf("%064d", 0), wantErr: "is not a valid namespace name"},
		{name: "default", namespace: "default", wantErr: "is a reserved Kubernetes namespace"},
		{name: "kube-system", namespace: "kube-system", wantErr: "is a reserved Kubernetes namespace"},
		{name: "kube-public", namespace: "kube-public", wantErr: "is a reserved Kubernetes namespace"},
		{name: "kube-node-lease", namespace: "kube-node-lease", wantErr: "is a reserved Kubernetes namespace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg nvcaconfig.Config
			cfg.Agent.RequestsNamespace = tt.namespace
			err := validateRequestsNamespaceConfig(cfg)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, isInvalidAgentConfigError(err), "rejection must keep the running agent")
			assert.ErrorContains(t, err, "worker.requestsNamespace")
			assert.ErrorContains(t, err, tt.wantErr)
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

// gxCacheOwnedKeys mirrors what setupRequestsNamespace declares: the GXCache key is owned as a label only.
var gxCacheOwnedKeys = namespaceOwnedKeys{labels: []string{clustermgmt.ShaderCacheLabelKey}}

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

		require.NoError(t, bc.createOrUpdateNamespace(ctx, desired, namespaceOwnedKeys{}))

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

		require.NoError(t, bc.createOrUpdateNamespace(ctx, desired, namespaceOwnedKeys{}))

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

		require.NoError(t, bc.createOrUpdateNamespace(ctx, desired, gxCacheOwnedKeys))

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

		require.NoError(t, bc.createOrUpdateNamespace(ctx, desired, gxCacheOwnedKeys))

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
		}, namespaceOwnedKeys{}))
		require.NoError(t, bc.createOrUpdateNamespace(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-ns"},
		}, namespaceOwnedKeys{}))

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

		require.NoError(t, bc.createOrUpdateNamespace(ctx, desired, namespaceOwnedKeys{}))

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
		}, namespaceOwnedKeys{}))

		for _, action := range clientset.Actions() {
			assert.NotEqual(t, "update", action.GetVerb(), "namespace was rewritten with no metadata change")
		}
	})

	t.Run("an optional owned label key does not remove the annotation of the same name", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "test-ns",
				Labels:      map[string]string{clustermgmt.ShaderCacheLabelKey: "true"},
				Annotations: map[string]string{clustermgmt.ShaderCacheLabelKey: "external"},
			},
		})

		desired := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-ns", Labels: getAppLabels()},
		}

		require.NoError(t, bc.createOrUpdateNamespace(ctx, desired, gxCacheOwnedKeys))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, "test-ns", metav1.GetOptions{})
		require.NoError(t, err)
		assert.NotContains(t, got.Labels, clustermgmt.ShaderCacheLabelKey)
		assert.Equal(t, "external", got.Annotations[clustermgmt.ShaderCacheLabelKey])
	})

	t.Run("lost create race falls through to the merge path", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t)

		creates := 0
		clientset.PrependReactor("create", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
			creates++

			// Simulate another writer creating the namespace between our failed read and our create.
			winner := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-ns",
					Labels:      map[string]string{externalLabelKey: "platform"},
					Annotations: map[string]string{externalAnnotationKey: "enabled"},
				},
			}
			if err := clientset.Tracker().Add(winner); err != nil {
				return true, nil, err
			}

			return true, nil, k8serrors.NewAlreadyExists(corev1.Resource("namespaces"), "test-ns")
		})

		require.NoError(t, bc.createOrUpdateNamespace(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-ns", Labels: getAppLabels()},
		}, namespaceOwnedKeys{}))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, "test-ns", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, 1, creates)
		assert.Equal(t, "platform", got.Labels[externalLabelKey])
		assert.Equal(t, "enabled", got.Annotations[externalAnnotationKey])
		assert.Equal(t, nvcaoptypes.NVCAModuleName, got.Labels[InstanceLabelKey])
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
		}, namespaceOwnedKeys{}))

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

		require.NoError(t, bc.setupRequestsNamespace(ctx, getRequestsNamespace(nb)))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "platform", got.Labels[externalLabelKey])
		assert.Equal(t, "enabled", got.Annotations[externalAnnotationKey])
		assert.Equal(t, nvcaoptypes.NVCAModuleName, got.Labels[ManagedbyLabelKey])
		assert.Equal(t, WorkloadInstanceTypeValuePodSpec, got.Labels[nvcatypes.WorkloadInstanceTypeLabel])
		assert.Equal(t, "true", got.Labels[clustermgmt.ShaderCacheLabelKey])
	})

	t.Run("missing requests namespace is created and marked operator-created", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t)

		require.NoError(t, bc.setupRequestsNamespace(ctx, "team-x"))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, "team-x", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "true", got.Annotations[nvcaoptypes.CreatedByOperatorAnnotation])
		assert.Equal(t, nvcaoptypes.NVCAModuleName, got.Labels[ManagedbyLabelKey])
		assert.Equal(t, WorkloadInstanceTypeValuePodSpec, got.Labels[nvcatypes.WorkloadInstanceTypeLabel])
		sa, err := clientset.CoreV1().ServiceAccounts("team-x").Get(ctx, "default", metav1.GetOptions{})
		require.NoError(t, err)
		require.NotNil(t, sa.AutomountServiceAccountToken)
		assert.False(t, *sa.AutomountServiceAccountToken)
	})

	t.Run("pre-existing requests namespace is labeled but not marked operator-created", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t, externalMetadata("team-x"))

		require.NoError(t, bc.setupRequestsNamespace(ctx, "team-x"))
		// A second sync must not mark it either.
		require.NoError(t, bc.setupRequestsNamespace(ctx, "team-x"))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, "team-x", metav1.GetOptions{})
		require.NoError(t, err)
		assert.NotContains(t, got.Annotations, nvcaoptypes.CreatedByOperatorAnnotation)
		assert.Equal(t, "enabled", got.Annotations[externalAnnotationKey])
		assert.Equal(t, nvcaoptypes.NVCAModuleName, got.Labels[ManagedbyLabelKey])
	})

	t.Run("requests namespace get failure is returned", func(t *testing.T) {
		bc, clientset := newNamespaceCache(t)
		clientset.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, k8serrors.NewForbidden(corev1.Resource("namespaces"), "team-x", fmt.Errorf("denied"))
		})

		err := bc.setupRequestsNamespace(ctx, "team-x")
		require.Error(t, err)
		assert.True(t, k8serrors.IsForbidden(err))
	})

	t.Run("requests namespace drops the gxcache label when disabled", func(t *testing.T) {
		existing := externalMetadata(getRequestsNamespace(nb))
		existing.Labels[clustermgmt.ShaderCacheLabelKey] = "true"
		bc, clientset := newNamespaceCache(t, existing)

		require.NoError(t, bc.setupRequestsNamespace(ctx, getRequestsNamespace(nb)))

		got, err := clientset.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.NoError(t, err)
		assert.NotContains(t, got.Labels, clustermgmt.ShaderCacheLabelKey)
		assert.Equal(t, "platform", got.Labels[externalLabelKey])
		assert.Equal(t, "enabled", got.Annotations[externalAnnotationKey])
	})
}
