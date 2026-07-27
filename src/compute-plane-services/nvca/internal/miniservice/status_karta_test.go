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
	"sort"
	"testing"

	kartav1alpha1 "github.com/run-ai/karta/pkg/api/runai/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

func TestGetEmbeddedKartaDefinitions(t *testing.T) {
	kartas, err := getEmbeddedKartaDefinitions()
	require.NoError(t, err)
	require.NotEmpty(t, kartas, "expected at least one embedded karta definition")

	for _, karta := range kartas {
		assert.NotEmpty(t, karta.Name, "embedded karta must have a name")
		assert.NotNil(t, karta.Spec.StructureDefinition.RootComponent.Kind,
			"embedded karta %s must have a root component kind", karta.Name)
		gvk := karta.Spec.StructureDefinition.RootComponent.Kind
		assert.NotEmpty(t, gvk.Group, "embedded karta %s root GVK group must not be empty", karta.Name)
		assert.NotEmpty(t, gvk.Version, "embedded karta %s root GVK version must not be empty", karta.Name)
		assert.NotEmpty(t, gvk.Kind, "embedded karta %s root GVK kind must not be empty", karta.Name)

		_, err := (&Reconciler{}).newKartaDefinedObjectStatusChecker(karta)
		assert.NoError(t, err)
	}
}

func TestLoadKartaDefinitions_EmbeddedOnly(t *testing.T) {
	kartas, err := loadKartaDefinitions(context.Background(), nil)
	require.NoError(t, err)
	require.NotEmpty(t, kartas)

	for _, karta := range kartas {
		assert.NotEmpty(t, karta.Name)
		assert.NotNil(t, karta.Spec.StructureDefinition.RootComponent.Kind)
	}
}

func TestLoadKartaDefinitions_LiveOverridesEmbedded(t *testing.T) {
	liveKarta := &kartav1alpha1.Karta{
		ObjectMeta: metav1.ObjectMeta{
			Name: "live-dynamo-override",
		},
		Spec: kartav1alpha1.KartaSpec{
			StructureDefinition: kartav1alpha1.StructureDefinition{
				RootComponent: kartav1alpha1.ComponentDefinition{
					Name: "dynamographdeployment",
					Kind: &kartav1alpha1.GroupVersionKind{
						Group:   "nvidia.com",
						Version: "v1alpha1",
						Kind:    "DynamoGraphDeployment",
					},
					StatusDefinition: &kartav1alpha1.StatusDefinition{
						PhaseDefinition: &kartav1alpha1.PhaseDefinition{
							Path: ".status.phase",
						},
						StatusMappings: kartav1alpha1.StatusMappings{
							Running: []kartav1alpha1.StatusMatcher{{ByPhase: "running"}},
							Failed:  []kartav1alpha1.StatusMatcher{{ByPhase: "error"}},
						},
					},
				},
			},
		},
	}

	kartas, err := loadKartaDefinitions(context.Background(), []*kartav1alpha1.Karta{liveKarta})
	require.NoError(t, err)
	require.NotEmpty(t, kartas)

	var foundOverride bool
	for _, karta := range kartas {
		gvk := karta.Spec.StructureDefinition.RootComponent.Kind
		if gvk != nil && gvk.Group == "nvidia.com" && gvk.Kind == "DynamoGraphDeployment" {
			assert.Equal(t, "live-dynamo-override", karta.Name,
				"live karta should override embedded for same GVK")
			foundOverride = true
			break
		}
	}
	assert.True(t, foundOverride, "expected live karta to override the embedded one for DynamoGraphDeployment GVK")
}

func TestLoadKartaDefinitions_LiveAddsNew(t *testing.T) {
	liveKarta := &kartav1alpha1.Karta{
		ObjectMeta: metav1.ObjectMeta{
			Name: "custom-workload-karta",
		},
		Spec: kartav1alpha1.KartaSpec{
			StructureDefinition: kartav1alpha1.StructureDefinition{
				RootComponent: kartav1alpha1.ComponentDefinition{
					Name: "customworkload",
					Kind: &kartav1alpha1.GroupVersionKind{
						Group:   "custom.example.io",
						Version: "v1",
						Kind:    "CustomWorkload",
					},
					StatusDefinition: &kartav1alpha1.StatusDefinition{
						PhaseDefinition: &kartav1alpha1.PhaseDefinition{
							Path: ".status.phase",
						},
						StatusMappings: kartav1alpha1.StatusMappings{
							Running: []kartav1alpha1.StatusMatcher{{ByPhase: "active"}},
							Failed:  []kartav1alpha1.StatusMatcher{{ByPhase: "failed"}},
						},
					},
				},
			},
		},
	}

	kartas, err := loadKartaDefinitions(context.Background(), []*kartav1alpha1.Karta{liveKarta})
	require.NoError(t, err)

	embeddedKartas, err := getEmbeddedKartaDefinitions()
	require.NoError(t, err)
	assert.Equal(t, len(embeddedKartas), len(kartas),
		"live karta with unknown GVK should NOT be added (only overrides embedded)")
}

func TestLoadKartaDefinitions_LiveWithNilKind(t *testing.T) {
	liveKarta := &kartav1alpha1.Karta{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bad-karta",
		},
		Spec: kartav1alpha1.KartaSpec{
			StructureDefinition: kartav1alpha1.StructureDefinition{
				RootComponent: kartav1alpha1.ComponentDefinition{
					Name: "no-kind",
					Kind: nil,
				},
			},
		},
	}

	_, err := loadKartaDefinitions(context.Background(), []*kartav1alpha1.Karta{liveKarta})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no root component kind")
}

func TestLoadKartaDefinitions_LiveWithEmptyGVKFields(t *testing.T) {
	tests := []struct {
		name  string
		karta *kartav1alpha1.Karta
	}{
		{
			name: "empty group",
			karta: &kartav1alpha1.Karta{
				ObjectMeta: metav1.ObjectMeta{Name: "empty-group"},
				Spec: kartav1alpha1.KartaSpec{
					StructureDefinition: kartav1alpha1.StructureDefinition{
						RootComponent: kartav1alpha1.ComponentDefinition{
							Name: "root",
							Kind: &kartav1alpha1.GroupVersionKind{Group: "", Version: "v1", Kind: "Foo"},
						},
					},
				},
			},
		},
		{
			name: "empty version",
			karta: &kartav1alpha1.Karta{
				ObjectMeta: metav1.ObjectMeta{Name: "empty-version"},
				Spec: kartav1alpha1.KartaSpec{
					StructureDefinition: kartav1alpha1.StructureDefinition{
						RootComponent: kartav1alpha1.ComponentDefinition{
							Name: "root",
							Kind: &kartav1alpha1.GroupVersionKind{Group: "foo.io", Version: "", Kind: "Foo"},
						},
					},
				},
			},
		},
		{
			name: "empty kind",
			karta: &kartav1alpha1.Karta{
				ObjectMeta: metav1.ObjectMeta{Name: "empty-kind"},
				Spec: kartav1alpha1.KartaSpec{
					StructureDefinition: kartav1alpha1.StructureDefinition{
						RootComponent: kartav1alpha1.ComponentDefinition{
							Name: "root",
							Kind: &kartav1alpha1.GroupVersionKind{Group: "foo.io", Version: "v1", Kind: ""},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadKartaDefinitions(context.Background(), []*kartav1alpha1.Karta{tt.karta})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is missing group, version, or kind")
		})
	}
}

func TestNewKartaDefinedObjectStatusChecker_NilKind(t *testing.T) {
	r := &Reconciler{}
	karta := &kartav1alpha1.Karta{
		ObjectMeta: metav1.ObjectMeta{Name: "test-karta"},
		Spec: kartav1alpha1.KartaSpec{
			StructureDefinition: kartav1alpha1.StructureDefinition{
				RootComponent: kartav1alpha1.ComponentDefinition{
					Name: "root",
					Kind: nil,
				},
			},
		},
	}

	_, err := r.newKartaDefinedObjectStatusChecker(karta)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no root component kind")
}

func TestNewKartaDefinedObjectStatusChecker_NilStatusDefinition(t *testing.T) {
	r := &Reconciler{}
	karta := &kartav1alpha1.Karta{
		ObjectMeta: metav1.ObjectMeta{Name: "test-karta"},
		Spec: kartav1alpha1.KartaSpec{
			StructureDefinition: kartav1alpha1.StructureDefinition{
				RootComponent: kartav1alpha1.ComponentDefinition{
					Name:             "root",
					Kind:             &kartav1alpha1.GroupVersionKind{Group: "foo.io", Version: "v1", Kind: "Foo"},
					StatusDefinition: nil,
				},
			},
		},
	}

	_, err := r.newKartaDefinedObjectStatusChecker(karta)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no root component status definition")
}

func TestNewKartaDefinedObjectStatusChecker_CheckMappings(t *testing.T) {
	r := &Reconciler{}

	// StatusMappings.Entries() always returns all status types (Running, Failed, etc.)
	// even with empty matcher slices. The validation in newKartaDefinedObjectStatusChecker
	// checks that Running and Failed appear in the entries, which they always do.
	tests := []struct {
		name        string
		mappings    kartav1alpha1.StatusMappings
		shouldError bool
	}{
		{
			name: "all required matchers",
			mappings: kartav1alpha1.StatusMappings{
				Failed:  []kartav1alpha1.StatusMatcher{{ByPhase: "failed"}},
				Running: []kartav1alpha1.StatusMatcher{{ByPhase: "running"}},
			},
			shouldError: false,
		},
		{
			name: "only failed has matchers",
			mappings: kartav1alpha1.StatusMappings{
				Failed: []kartav1alpha1.StatusMatcher{{ByPhase: "failed"}},
			},
			shouldError: true,
		},
		{
			name: "only running has matchers",
			mappings: kartav1alpha1.StatusMappings{
				Running: []kartav1alpha1.StatusMatcher{{ByPhase: "running"}},
			},
			shouldError: true,
		},
		{
			name:        "empty mappings",
			mappings:    kartav1alpha1.StatusMappings{},
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			karta := &kartav1alpha1.Karta{
				ObjectMeta: metav1.ObjectMeta{Name: "test-karta"},
				Spec: kartav1alpha1.KartaSpec{
					StructureDefinition: kartav1alpha1.StructureDefinition{
						RootComponent: kartav1alpha1.ComponentDefinition{
							Name: "root",
							Kind: &kartav1alpha1.GroupVersionKind{Group: "foo.io", Version: "v1", Kind: "Foo"},
							StatusDefinition: &kartav1alpha1.StatusDefinition{
								PhaseDefinition: &kartav1alpha1.PhaseDefinition{Path: ".status.phase"},
								StatusMappings:  tt.mappings,
							},
						},
					},
				},
			}

			checkFunc, err := r.newKartaDefinedObjectStatusChecker(karta)
			if tt.shouldError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, checkFunc)
			}
		})
	}
}

func TestNewKartaDefinedObjectStatusChecker_ValidKarta(t *testing.T) {
	r := &Reconciler{}

	karta := &kartav1alpha1.Karta{
		ObjectMeta: metav1.ObjectMeta{Name: "valid-karta"},
		Spec: kartav1alpha1.KartaSpec{
			StructureDefinition: kartav1alpha1.StructureDefinition{
				RootComponent: kartav1alpha1.ComponentDefinition{
					Name: "root",
					Kind: &kartav1alpha1.GroupVersionKind{Group: "nvidia.com", Version: "v1alpha1", Kind: "DynamoGraphDeployment"},
					StatusDefinition: &kartav1alpha1.StatusDefinition{
						PhaseDefinition: &kartav1alpha1.PhaseDefinition{Path: ".status.state"},
						StatusMappings: kartav1alpha1.StatusMappings{
							Running: []kartav1alpha1.StatusMatcher{{ByPhase: "successful"}},
							Failed:  []kartav1alpha1.StatusMatcher{{ByPhase: "failed"}},
						},
					},
				},
			},
		},
	}

	checkFunc, err := r.newKartaDefinedObjectStatusChecker(karta)
	require.NoError(t, err)
	assert.NotNil(t, checkFunc)
}

func TestNewKartaDefinedObjectStatusChecker_StatusChecking(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, kartav1alpha1.AddToScheme(scheme))

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &Reconciler{
		Client: c,
	}

	karta := &kartav1alpha1.Karta{
		ObjectMeta: metav1.ObjectMeta{Name: "test-karta"},
		Spec: kartav1alpha1.KartaSpec{
			StructureDefinition: kartav1alpha1.StructureDefinition{
				RootComponent: kartav1alpha1.ComponentDefinition{
					Name: "root",
					Kind: &kartav1alpha1.GroupVersionKind{Group: "nvidia.com", Version: "v1alpha1", Kind: "DynamoGraphDeployment"},
					StatusDefinition: &kartav1alpha1.StatusDefinition{
						PhaseDefinition: &kartav1alpha1.PhaseDefinition{Path: ".status.state"},
						StatusMappings: kartav1alpha1.StatusMappings{
							Initializing: []kartav1alpha1.StatusMatcher{{ByPhase: "pending"}},
							Running:      []kartav1alpha1.StatusMatcher{{ByPhase: "successful"}},
							Failed:       []kartav1alpha1.StatusMatcher{{ByPhase: "failed"}},
							Completed:    []kartav1alpha1.StatusMatcher{{ByPhase: "completed"}},
							Degraded:     []kartav1alpha1.StatusMatcher{{ByPhase: "degraded"}},
						},
					},
				},
			},
		},
	}

	checkFunc, err := r.newKartaDefinedObjectStatusChecker(karta)
	require.NoError(t, err)

	tests := []struct {
		name            string
		statusState     string
		expectedStatus  string
		expectedPending bool
		expectedBad     bool
	}{
		{
			name:           "running status",
			statusState:    "successful",
			expectedStatus: "running",
		},
		{
			name:           "failed status",
			statusState:    "failed",
			expectedStatus: statusFailed,
			expectedBad:    true,
		},
		{
			name:           "completed status",
			statusState:    "completed",
			expectedStatus: statusSucceeded,
		},
		{
			name:           "degraded status",
			statusState:    "degraded",
			expectedStatus: statusDegradedWorker,
			expectedBad:    true,
		},
		{
			name:            "initializing status",
			statusState:     "pending",
			expectedStatus:  statusStarting,
			expectedPending: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "nvidia.com/v1alpha1",
					"kind":       "DynamoGraphDeployment",
					"metadata": map[string]any{
						"name":      "test-deploy",
						"namespace": "test-ns",
					},
					"status": map[string]any{
						"state": tt.statusState,
					},
				},
			}

			ctx := withStatusContext(context.Background(), &statusContext{
				namespace: "test-ns",
				events:    []corev1.Event{},
			})

			objStatus, err := checkFunc(ctx, obj, &nvcav2beta1.ICMSRequest{})
			require.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, objStatus.Status)
			assert.Equal(t, tt.expectedPending, objStatus.Pending)
			assert.Equal(t, tt.expectedBad, objStatus.TerminalBad)
			assert.Equal(t, obj, objStatus.Object)
		})
	}
}

func TestNewKartaDefinedObjectStatusChecker_WithAbnormalEvents(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, kartav1alpha1.AddToScheme(scheme))

	karta := &kartav1alpha1.Karta{
		ObjectMeta: metav1.ObjectMeta{Name: "test-karta"},
		Spec: kartav1alpha1.KartaSpec{
			StructureDefinition: kartav1alpha1.StructureDefinition{
				RootComponent: kartav1alpha1.ComponentDefinition{
					Name: "root",
					Kind: &kartav1alpha1.GroupVersionKind{Group: "nvidia.com", Version: "v1alpha1", Kind: "DynamoGraphDeployment"},
					StatusDefinition: &kartav1alpha1.StatusDefinition{
						PhaseDefinition: &kartav1alpha1.PhaseDefinition{Path: ".status.state"},
						StatusMappings: kartav1alpha1.StatusMappings{
							Running: []kartav1alpha1.StatusMatcher{{ByPhase: "successful"}},
							Failed:  []kartav1alpha1.StatusMatcher{{ByPhase: "failed"}},
						},
					},
				},
			},
		},
	}

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "nvidia.com/v1alpha1",
			"kind":       "DynamoGraphDeployment",
			"metadata": map[string]any{
				"name":      "test-deploy",
				"namespace": "test-ns",
			},
			"status": map[string]any{
				"state": "successful",
			},
		},
	}

	errorEvent := corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-event",
			Namespace: "test-ns",
		},
		InvolvedObject: corev1.ObjectReference{
			Name:       "test-deploy",
			Namespace:  "test-ns",
			Kind:       "DynamoGraphDeployment",
			APIVersion: "nvidia.com/v1alpha1",
		},
		Type:    corev1.EventTypeWarning,
		Reason:  "FailedCreate",
		Message: `create Pod foo in DynamoGraphDeployment test-deploy failed error: pods "foo" is forbidden: exceeded quota`,
	}

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &Reconciler{
		Client: c,
	}

	checkFunc, err := r.newKartaDefinedObjectStatusChecker(karta)
	require.NoError(t, err)

	ctx := withStatusContext(context.Background(), &statusContext{
		namespace: "test-ns",
		events:    []corev1.Event{errorEvent},
	})

	objStatus, err := checkFunc(ctx, obj, &nvcav2beta1.ICMSRequest{})
	require.NoError(t, err)
	assert.True(t, objStatus.TerminalBad, "should be terminal bad due to error events")
	assert.Equal(t, statusFailed, objStatus.Status)
}

func TestNewKartaDefinedObjectStatusChecker_WithNonErrorEvents(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, kartav1alpha1.AddToScheme(scheme))

	karta := &kartav1alpha1.Karta{
		ObjectMeta: metav1.ObjectMeta{Name: "test-karta"},
		Spec: kartav1alpha1.KartaSpec{
			StructureDefinition: kartav1alpha1.StructureDefinition{
				RootComponent: kartav1alpha1.ComponentDefinition{
					Name: "root",
					Kind: &kartav1alpha1.GroupVersionKind{Group: "nvidia.com", Version: "v1alpha1", Kind: "DynamoGraphDeployment"},
					StatusDefinition: &kartav1alpha1.StatusDefinition{
						PhaseDefinition: &kartav1alpha1.PhaseDefinition{Path: ".status.state"},
						StatusMappings: kartav1alpha1.StatusMappings{
							Running: []kartav1alpha1.StatusMatcher{{ByPhase: "successful"}},
							Failed:  []kartav1alpha1.StatusMatcher{{ByPhase: "failed"}},
						},
					},
				},
			},
		},
	}

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "nvidia.com/v1alpha1",
			"kind":       "DynamoGraphDeployment",
			"metadata": map[string]any{
				"name":      "test-deploy",
				"namespace": "test-ns",
			},
			"status": map[string]any{
				"state": "successful",
			},
		},
	}

	warningEvent := corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "warning-event",
			Namespace: "test-ns",
		},
		InvolvedObject: corev1.ObjectReference{
			Name:       "test-deploy",
			Namespace:  "test-ns",
			Kind:       "DynamoGraphDeployment",
			APIVersion: "nvidia.com/v1alpha1",
		},
		Type:    corev1.EventTypeWarning,
		Reason:  "SomeWarning",
		Message: "something not great but not fatal",
	}

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &Reconciler{
		Client: c,
	}

	checkFunc, err := r.newKartaDefinedObjectStatusChecker(karta)
	require.NoError(t, err)

	ctx := withStatusContext(context.Background(), &statusContext{
		namespace: "test-ns",
		events:    []corev1.Event{warningEvent},
	})

	objStatus, err := checkFunc(ctx, obj, &nvcav2beta1.ICMSRequest{})
	require.NoError(t, err)
	assert.False(t, objStatus.TerminalBad, "non-error warning events should not make status terminal")
	assert.Equal(t, "running", objStatus.Status)
	assert.NotEmpty(t, objStatus.AbnormalEvents, "abnormal events should still be collected")
}

func TestNewKartaDefinedObjectStatusChecker_AlreadyTerminalNotOverridden(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, kartav1alpha1.AddToScheme(scheme))

	karta := &kartav1alpha1.Karta{
		ObjectMeta: metav1.ObjectMeta{Name: "test-karta"},
		Spec: kartav1alpha1.KartaSpec{
			StructureDefinition: kartav1alpha1.StructureDefinition{
				RootComponent: kartav1alpha1.ComponentDefinition{
					Name: "root",
					Kind: &kartav1alpha1.GroupVersionKind{Group: "nvidia.com", Version: "v1alpha1", Kind: "DynamoGraphDeployment"},
					StatusDefinition: &kartav1alpha1.StatusDefinition{
						PhaseDefinition: &kartav1alpha1.PhaseDefinition{Path: ".status.state"},
						StatusMappings: kartav1alpha1.StatusMappings{
							Running: []kartav1alpha1.StatusMatcher{{ByPhase: "successful"}},
							Failed:  []kartav1alpha1.StatusMatcher{{ByPhase: "failed"}},
						},
					},
				},
			},
		},
	}

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "nvidia.com/v1alpha1",
			"kind":       "DynamoGraphDeployment",
			"metadata": map[string]any{
				"name":      "test-deploy",
				"namespace": "test-ns",
			},
			"status": map[string]any{
				"state": "failed",
			},
		},
	}

	errorEvent := corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-event",
			Namespace: "test-ns",
		},
		InvolvedObject: corev1.ObjectReference{
			Name:       "test-deploy",
			Namespace:  "test-ns",
			Kind:       "DynamoGraphDeployment",
			APIVersion: "nvidia.com/v1alpha1",
		},
		Type:    corev1.EventTypeWarning,
		Reason:  "FailedCreate",
		Message: `create Pod foo in DynamoGraphDeployment test-deploy failed error: pods "foo" is forbidden: exceeded quota`,
	}

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &Reconciler{
		Client: c,
	}

	checkFunc, err := r.newKartaDefinedObjectStatusChecker(karta)
	require.NoError(t, err)

	ctx := withStatusContext(context.Background(), &statusContext{
		namespace: "test-ns",
		events:    []corev1.Event{errorEvent},
	})

	objStatus, err := checkFunc(ctx, obj, &nvcav2beta1.ICMSRequest{})
	require.NoError(t, err)
	assert.True(t, objStatus.TerminalBad)
	assert.Equal(t, statusFailed, objStatus.Status,
		"already-terminal status should not be overridden by toTerminalEventStatus")
}

func TestNewKartaDefinedObjectStatusChecker_UnmatchedPhaseDefaultsToInitializing(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, kartav1alpha1.AddToScheme(scheme))

	karta := &kartav1alpha1.Karta{
		ObjectMeta: metav1.ObjectMeta{Name: "test-karta"},
		Spec: kartav1alpha1.KartaSpec{
			StructureDefinition: kartav1alpha1.StructureDefinition{
				RootComponent: kartav1alpha1.ComponentDefinition{
					Name: "root",
					Kind: &kartav1alpha1.GroupVersionKind{Group: "nvidia.com", Version: "v1alpha1", Kind: "DynamoGraphDeployment"},
					StatusDefinition: &kartav1alpha1.StatusDefinition{
						PhaseDefinition: &kartav1alpha1.PhaseDefinition{Path: ".status.state"},
						StatusMappings: kartav1alpha1.StatusMappings{
							Running: []kartav1alpha1.StatusMatcher{{ByPhase: "successful"}},
							Failed:  []kartav1alpha1.StatusMatcher{{ByPhase: "failed"}},
						},
					},
				},
			},
		},
	}

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "nvidia.com/v1alpha1",
			"kind":       "DynamoGraphDeployment",
			"metadata": map[string]any{
				"name":      "test-deploy",
				"namespace": "test-ns",
			},
			"status": map[string]any{
				"state": "unknown-phase",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &Reconciler{
		Client: c,
	}

	checkFunc, err := r.newKartaDefinedObjectStatusChecker(karta)
	require.NoError(t, err)

	ctx := withStatusContext(context.Background(), &statusContext{
		namespace: "test-ns",
		events:    []corev1.Event{},
	})

	// When no specific matcher matches the phase, karta returns UndefinedStatus
	// mapping to "undefined" + Pending.
	objStatus, err := checkFunc(ctx, obj, &nvcav2beta1.ICMSRequest{})
	require.NoError(t, err)
	assert.Equal(t, "undefined", objStatus.Status)
	assert.True(t, objStatus.Pending)
	assert.False(t, objStatus.TerminalBad)
}

func TestNewKartaDefinedObjectStatusChecker_WithEmbeddedKarta(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, kartav1alpha1.AddToScheme(scheme))

	kartas, err := getEmbeddedKartaDefinitions()
	require.NoError(t, err)
	require.NotEmpty(t, kartas)

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &Reconciler{
		Client: c,
	}

	for _, karta := range kartas {
		t.Run(karta.Name, func(t *testing.T) {
			checkFunc, err := r.newKartaDefinedObjectStatusChecker(karta)
			require.NoError(t, err)
			assert.NotNil(t, checkFunc)
		})
	}
}

func TestNewKartaDefinedObjectStatusChecker_EmbeddedDynamoRunning(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, kartav1alpha1.AddToScheme(scheme))

	kartas, err := getEmbeddedKartaDefinitions()
	require.NoError(t, err)

	var dynamoKarta *kartav1alpha1.Karta
	for _, k := range kartas {
		if k.Spec.StructureDefinition.RootComponent.Kind != nil &&
			k.Spec.StructureDefinition.RootComponent.Kind.Kind == "DynamoGraphDeployment" {
			dynamoKarta = k
			break
		}
	}
	require.NotNil(t, dynamoKarta, "should find the DynamoGraphDeployment karta")

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &Reconciler{
		Client: c,
	}

	checkFunc, err := r.newKartaDefinedObjectStatusChecker(dynamoKarta)
	require.NoError(t, err)

	tests := []struct {
		name            string
		state           string
		expectedStatus  string
		expectedPending bool
		expectedBad     bool
	}{
		{
			name:           "successful maps to running",
			state:          "successful",
			expectedStatus: "running",
		},
		{
			name:           "failed maps to failed",
			state:          "failed",
			expectedStatus: statusFailed,
			expectedBad:    true,
		},
		{
			name:            "pending maps to initializing",
			state:           "pending",
			expectedStatus:  statusStarting,
			expectedPending: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "nvidia.com/v1alpha1",
					"kind":       "DynamoGraphDeployment",
					"metadata": map[string]any{
						"name":      "my-dynamo-deploy",
						"namespace": "default",
					},
					"status": map[string]any{
						"state": tt.state,
					},
				},
			}

			ctx := withStatusContext(context.Background(), &statusContext{
				namespace: "default",
				events:    []corev1.Event{},
			})

			objStatus, err := checkFunc(ctx, obj, &nvcav2beta1.ICMSRequest{})
			require.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, objStatus.Status)
			assert.Equal(t, tt.expectedPending, objStatus.Pending)
			assert.Equal(t, tt.expectedBad, objStatus.TerminalBad)
		})
	}
}

func TestLoadKartaDefinitions_WithLiveKartaFromFakeClient(t *testing.T) {
	liveKartas := []*kartav1alpha1.Karta{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "live-dynamo-v2",
			},
			Spec: kartav1alpha1.KartaSpec{
				StructureDefinition: kartav1alpha1.StructureDefinition{
					RootComponent: kartav1alpha1.ComponentDefinition{
						Name: "dynamographdeployment",
						Kind: &kartav1alpha1.GroupVersionKind{
							Group:   "nvidia.com",
							Version: "v1alpha1",
							Kind:    "DynamoGraphDeployment",
						},
						StatusDefinition: &kartav1alpha1.StatusDefinition{
							PhaseDefinition: &kartav1alpha1.PhaseDefinition{Path: ".status.phase"},
							StatusMappings: kartav1alpha1.StatusMappings{
								Running:   []kartav1alpha1.StatusMatcher{{ByPhase: "active"}},
								Failed:    []kartav1alpha1.StatusMatcher{{ByPhase: "error"}},
								Completed: []kartav1alpha1.StatusMatcher{{ByPhase: "done"}},
							},
						},
					},
				},
			},
		},
	}

	kartas, err := loadKartaDefinitions(context.Background(), liveKartas)
	require.NoError(t, err)
	require.NotEmpty(t, kartas)

	var dynamoKarta *kartav1alpha1.Karta
	for _, k := range kartas {
		gvk := k.Spec.StructureDefinition.RootComponent.Kind
		if gvk != nil && gvk.Kind == "DynamoGraphDeployment" {
			dynamoKarta = k
			break
		}
	}
	require.NotNil(t, dynamoKarta)
	assert.Equal(t, "live-dynamo-v2", dynamoKarta.Name,
		"live karta should override embedded one with same GVK")
	assert.Equal(t, ".status.phase", dynamoKarta.Spec.StructureDefinition.RootComponent.StatusDefinition.PhaseDefinition.Path,
		"live karta should have its own status definition path")
}

func TestSetKartaStatusOnObjectStatus(t *testing.T) {
	tests := []struct {
		name             string
		matchedStatuses  []kartav1alpha1.ResourceStatus
		expectedStatus   string
		expectedPending  bool
		expectedBad      bool
		expectedWinnerRs kartav1alpha1.ResourceStatus
	}{
		{
			name:             "running",
			matchedStatuses:  []kartav1alpha1.ResourceStatus{kartav1alpha1.RunningStatus},
			expectedStatus:   statusRunning,
			expectedWinnerRs: kartav1alpha1.RunningStatus,
		},
		{
			name:             "completed",
			matchedStatuses:  []kartav1alpha1.ResourceStatus{kartav1alpha1.CompletedStatus},
			expectedStatus:   statusSucceeded,
			expectedWinnerRs: kartav1alpha1.CompletedStatus,
		},
		{
			name:             "failed",
			matchedStatuses:  []kartav1alpha1.ResourceStatus{kartav1alpha1.FailedStatus},
			expectedStatus:   statusFailed,
			expectedBad:      true,
			expectedWinnerRs: kartav1alpha1.FailedStatus,
		},
		{
			name:             "degraded",
			matchedStatuses:  []kartav1alpha1.ResourceStatus{kartav1alpha1.DegradedStatus},
			expectedStatus:   statusDegradedWorker,
			expectedBad:      true,
			expectedWinnerRs: kartav1alpha1.DegradedStatus,
		},
		{
			name:             "undefined",
			matchedStatuses:  []kartav1alpha1.ResourceStatus{kartav1alpha1.UndefinedStatus},
			expectedStatus:   "undefined",
			expectedPending:  true,
			expectedWinnerRs: kartav1alpha1.UndefinedStatus,
		},
		{
			name:             "initializing",
			matchedStatuses:  []kartav1alpha1.ResourceStatus{kartav1alpha1.InitializingStatus},
			expectedStatus:   statusStarting,
			expectedPending:  true,
			expectedWinnerRs: kartav1alpha1.InitializingStatus,
		},
		{
			name:             "unknown status hits default",
			matchedStatuses:  []kartav1alpha1.ResourceStatus{kartav1alpha1.ResourceStatus("SomethingNew")},
			expectedStatus:   statusStarting,
			expectedPending:  true,
			expectedWinnerRs: kartav1alpha1.ResourceStatus("SomethingNew"),
		},
		{
			name:             "failed outranks running",
			matchedStatuses:  []kartav1alpha1.ResourceStatus{kartav1alpha1.RunningStatus, kartav1alpha1.FailedStatus},
			expectedStatus:   statusFailed,
			expectedBad:      true,
			expectedWinnerRs: kartav1alpha1.FailedStatus,
		},
		{
			name:             "degraded outranks running",
			matchedStatuses:  []kartav1alpha1.ResourceStatus{kartav1alpha1.RunningStatus, kartav1alpha1.DegradedStatus},
			expectedStatus:   statusDegradedWorker,
			expectedBad:      true,
			expectedWinnerRs: kartav1alpha1.DegradedStatus,
		},
		{
			name:             "failed outranks degraded",
			matchedStatuses:  []kartav1alpha1.ResourceStatus{kartav1alpha1.DegradedStatus, kartav1alpha1.FailedStatus},
			expectedStatus:   statusFailed,
			expectedBad:      true,
			expectedWinnerRs: kartav1alpha1.FailedStatus,
		},
		{
			name:             "completed outranks running",
			matchedStatuses:  []kartav1alpha1.ResourceStatus{kartav1alpha1.RunningStatus, kartav1alpha1.CompletedStatus},
			expectedStatus:   statusSucceeded,
			expectedWinnerRs: kartav1alpha1.CompletedStatus,
		},
		{
			name:             "running outranks initializing",
			matchedStatuses:  []kartav1alpha1.ResourceStatus{kartav1alpha1.InitializingStatus, kartav1alpha1.RunningStatus},
			expectedStatus:   statusRunning,
			expectedWinnerRs: kartav1alpha1.RunningStatus,
		},
		{
			name:             "initializing outranks undefined",
			matchedStatuses:  []kartav1alpha1.ResourceStatus{kartav1alpha1.UndefinedStatus, kartav1alpha1.InitializingStatus},
			expectedStatus:   statusStarting,
			expectedPending:  true,
			expectedWinnerRs: kartav1alpha1.InitializingStatus,
		},
		{
			name: "failed outranks all others",
			matchedStatuses: []kartav1alpha1.ResourceStatus{
				kartav1alpha1.UndefinedStatus,
				kartav1alpha1.InitializingStatus,
				kartav1alpha1.RunningStatus,
				kartav1alpha1.CompletedStatus,
				kartav1alpha1.DegradedStatus,
				kartav1alpha1.FailedStatus,
			},
			expectedStatus:   statusFailed,
			expectedBad:      true,
			expectedWinnerRs: kartav1alpha1.FailedStatus,
		},
		{
			name: "degraded outranks completed and running",
			matchedStatuses: []kartav1alpha1.ResourceStatus{
				kartav1alpha1.CompletedStatus,
				kartav1alpha1.RunningStatus,
				kartav1alpha1.DegradedStatus,
			},
			expectedStatus:   statusDegradedWorker,
			expectedBad:      true,
			expectedWinnerRs: kartav1alpha1.DegradedStatus,
		},
		{
			name:             "single status - no sorting needed",
			matchedStatuses:  []kartav1alpha1.ResourceStatus{kartav1alpha1.RunningStatus},
			expectedStatus:   statusRunning,
			expectedWinnerRs: kartav1alpha1.RunningStatus,
		},
		{
			name: "reversed input order - sort picks highest",
			matchedStatuses: []kartav1alpha1.ResourceStatus{
				kartav1alpha1.FailedStatus,
				kartav1alpha1.UndefinedStatus,
				kartav1alpha1.RunningStatus,
			},
			expectedStatus:   statusFailed,
			expectedBad:      true,
			expectedWinnerRs: kartav1alpha1.FailedStatus,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Replicate the ranking logic from newKartaDefinedObjectStatusChecker
			statuses := make([]kartav1alpha1.ResourceStatus, len(tt.matchedStatuses))
			copy(statuses, tt.matchedStatuses)

			sort.Slice(statuses, func(i, j int) bool {
				return kartaStatusRanks[statuses[i]] < kartaStatusRanks[statuses[j]]
			})

			winner := statuses[len(statuses)-1]
			assert.Equal(t, tt.expectedWinnerRs, winner, "highest-ranked status should win")

			objStatus := &ObjectStatus{}
			setKartaStatusOnObjectStatus(objStatus, winner)

			assert.Equal(t, tt.expectedStatus, objStatus.Status)
			assert.Equal(t, tt.expectedPending, objStatus.Pending)
			assert.Equal(t, tt.expectedBad, objStatus.TerminalBad)
		})
	}
}
