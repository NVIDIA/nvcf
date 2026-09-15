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

package pod

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	// testNamespace is the namespace used for testing pod cleanup
	testNamespace = "nvcf-backend"
)

// mockICMSRequestGetter implements ICMSRequestGetter for testing
type mockICMSRequestGetter struct {
	requests map[string]*nvcav2beta1.ICMSRequest
	errors   map[string]error
}

func (m *mockICMSRequestGetter) GetICMSRequest(ctx context.Context, name string) (*nvcav2beta1.ICMSRequest, error) {
	if err, exists := m.errors[name]; exists {
		return nil, err
	}
	if req, exists := m.requests[name]; exists {
		return req, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: "nvca.nvcf.nvidia.io", Resource: "icmsrequests"}, name)
}

// Test constructor
func TestNewCleaner(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	icmsGetter := &mockICMSRequestGetter{
		requests: make(map[string]*nvcav2beta1.ICMSRequest),
	}

	cleaner := NewCleaner(k8sClient, icmsGetter, nil, testNamespace)

	assert.NotNil(t, cleaner)
	assert.Equal(t, k8sClient, cleaner.k8sClient)
	assert.Equal(t, icmsGetter, cleaner.icmsRequestGetter)
	assert.Equal(t, testNamespace, cleaner.podsNamespace)
}

// Test Name method
func TestCleaner_Name(t *testing.T) {
	cleaner := &Cleaner{}
	assert.Equal(t, "PodCleaner", cleaner.Name())
}

// Test collectOrphanedPods method
func TestCleaner_collectOrphanedPods(t *testing.T) {
	tests := []struct {
		name              string
		pods              []corev1.Pod
		icmsRequests      map[string]*nvcav2beta1.ICMSRequest
		icmsRequestErrors map[string]error
		expectedOrphaned  int
		expectError       bool
		simulateListError bool
	}{
		{
			name:             "no pods in namespace",
			pods:             []corev1.Pod{},
			expectedOrphaned: 0,
			expectError:      false,
		},
		{
			name: "pod without owner references",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-without-owner",
						Namespace: testNamespace,
					},
				},
			},
			expectedOrphaned: 0,
			expectError:      false,
		},
		{
			name: "pod with non-ICMSRequest owner",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-with-other-owner",
						Namespace: testNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "ReplicaSet",
								Name:       "some-replicaset",
							},
						},
					},
				},
			},
			expectedOrphaned: 0,
			expectError:      false,
		},
		{
			name: "pod with ICMSRequest owner that exists",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-with-spotrequest",
						Namespace: testNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
								Kind:       "ICMSRequest",
								Name:       "sr-66ee0f88-415e-47dd-9852-9db1a6ae665a",
							},
						},
					},
				},
			},
			icmsRequests: map[string]*nvcav2beta1.ICMSRequest{
				"sr-66ee0f88-415e-47dd-9852-9db1a6ae665a": {
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sr-66ee0f88-415e-47dd-9852-9db1a6ae665a",
						Namespace: types.DefaultICMSRequestNamespace,
					},
				},
			},
			expectedOrphaned: 0,
			expectError:      false,
		},
		{
			name: "orphaned pod - ICMSRequest missing",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "orphaned-pod",
						Namespace: testNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
								Kind:       "ICMSRequest",
								Name:       "sr-missing",
							},
						},
					},
				},
			},
			icmsRequests:     map[string]*nvcav2beta1.ICMSRequest{},
			expectedOrphaned: 1,
			expectError:      false,
		},
		{
			name: "error checking ICMSRequest but not not-found",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: testNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
								Kind:       "ICMSRequest",
								Name:       "sr-error",
							},
						},
					},
				},
			},
			icmsRequestErrors: map[string]error{
				"sr-error": errors.New("internal server error"),
			},
			expectedOrphaned: 0,
			expectError:      false,
		},
		{
			name:              "error listing pods",
			pods:              []corev1.Pod{},
			simulateListError: true,
			expectError:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake k8s client
			objects := make([]runtime.Object, len(tt.pods))
			for i := range tt.pods {
				objects[i] = &tt.pods[i]
			}
			k8sClient := fake.NewSimpleClientset(objects...)

			// Simulate list error if needed
			if tt.simulateListError {
				k8sClient.PrependReactor("list", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("failed to list pods")
				})
			}

			// Create mock ICMSRequest getter
			mockICMSGetter := &mockICMSRequestGetter{
				requests: tt.icmsRequests,
				errors:   tt.icmsRequestErrors,
			}

			// Create cleaner and test
			cleaner := NewCleaner(k8sClient, mockICMSGetter, nil, testNamespace)
			orphaned, err := cleaner.collectOrphanedPods(ctx)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedOrphaned, len(orphaned))
		})
	}
}

// Test isOrphaned edge cases
func TestCleaner_isOrphaned(t *testing.T) {
	tests := []struct {
		name                  string
		pod                   *corev1.Pod
		icmsRequestExists     bool
		icmsRequestCheckError error
		expected              bool
	}{
		{
			name: "pod without owner references",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: testNamespace,
				},
			},
			expected: false,
		},
		{
			name: "pod with non-ICMSRequest owner",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: testNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "ReplicaSet",
							Name:       "some-replicaset",
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "orphaned - ICMSRequest does not exist",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: testNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
							Kind:       "ICMSRequest",
							Name:       "sr-missing",
						},
					},
				},
			},
			icmsRequestExists: false,
			expected:          true,
		},
		{
			name: "not orphaned - ICMSRequest exists",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: testNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
							Kind:       "ICMSRequest",
							Name:       "sr-existing",
						},
					},
				},
			},
			icmsRequestExists: true,
			expected:          false,
		},
		{
			name: "ICMSRequest check error - not orphaned",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: testNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
							Kind:       "ICMSRequest",
							Name:       "sr-error",
						},
					},
				},
			},
			icmsRequestExists:     false,
			icmsRequestCheckError: errors.New("internal server error"),
			expected:              false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mock ICMSRequest getter
			mockICMSGetter := &mockICMSRequestGetter{
				requests: make(map[string]*nvcav2beta1.ICMSRequest),
				errors:   make(map[string]error),
			}

			// Extract ICMSRequest name from owner references
			var icmsRequestName string
			for _, owner := range tt.pod.OwnerReferences {
				if owner.Kind == "ICMSRequest" && owner.APIVersion == "nvca.nvcf.nvidia.io/v2beta1" {
					icmsRequestName = owner.Name
					break
				}
			}

			if tt.icmsRequestExists && icmsRequestName != "" {
				mockICMSGetter.requests[icmsRequestName] = &nvcav2beta1.ICMSRequest{
					ObjectMeta: metav1.ObjectMeta{
						Name:      icmsRequestName,
						Namespace: types.DefaultICMSRequestNamespace,
					},
				}
			}

			if tt.icmsRequestCheckError != nil && icmsRequestName != "" {
				mockICMSGetter.errors[icmsRequestName] = tt.icmsRequestCheckError
			}

			// Create cleaner and test
			cleaner := NewCleaner(fake.NewSimpleClientset(), mockICMSGetter, nil, testNamespace)
			result := cleaner.isOrphaned(ctx, tt.pod)

			assert.Equal(t, tt.expected, result)
		})
	}
}

// Test cleanupPod edge cases
func TestCleaner_cleanupPod(t *testing.T) {
	tests := []struct {
		name                   string
		pod                    *corev1.Pod
		simulateUpdateError    bool
		simulateDeleteError    bool
		simulateAlreadyDeleted bool
		expectError            bool
	}{
		{
			name: "successful cleanup without finalizers",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: testNamespace,
				},
			},
			expectError: false,
		},
		{
			name: "successful cleanup with finalizers",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pod-with-finalizers",
					Namespace:  testNamespace,
					Finalizers: []string{"test-finalizer"},
				},
			},
			expectError: false,
		},
		{
			name: "finalizer update error",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pod",
					Namespace:  testNamespace,
					Finalizers: []string{"test-finalizer"},
				},
			},
			simulateUpdateError: true,
			expectError:         true,
		},
		{
			name: "delete error",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: testNamespace,
				},
			},
			simulateDeleteError: true,
			expectError:         true,
		},
		{
			name: "already deleted during cleanup",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: testNamespace,
				},
			},
			simulateAlreadyDeleted: true,
			expectError:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake k8s client
			k8sClient := fake.NewSimpleClientset(tt.pod)

			// Setup error simulation
			if tt.simulateUpdateError {
				k8sClient.PrependReactor("update", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("update failed")
				})
			}

			if tt.simulateDeleteError {
				k8sClient.PrependReactor("delete", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("delete failed")
				})
			}

			if tt.simulateAlreadyDeleted {
				k8sClient.PrependReactor("delete", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, tt.pod.Name)
				})
			}

			// Create cleaner and test
			cleaner := NewCleaner(k8sClient, &mockICMSRequestGetter{}, nil, testNamespace)
			err := cleaner.cleanupPod(ctx, tt.pod)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// Test executeCleanup method
func TestCleaner_executeCleanup(t *testing.T) {
	tests := []struct {
		name                  string
		orphanedPods          []corev1.Pod
		simulateCleanupErrors []string // Pod names that should fail cleanup
		expectNoError         bool
	}{
		{
			name:          "no orphaned pods",
			orphanedPods:  []corev1.Pod{},
			expectNoError: true,
		},
		{
			name: "successful cleanup of multiple pods",
			orphanedPods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "orphaned-pod-1",
						Namespace: testNamespace,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "orphaned-pod-2",
						Namespace: testNamespace,
					},
				},
			},
			expectNoError: true,
		},
		{
			name: "cleanup with some errors",
			orphanedPods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "orphaned-pod-success",
						Namespace: testNamespace,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "orphaned-pod-error",
						Namespace: testNamespace,
					},
				},
			},
			simulateCleanupErrors: []string{"orphaned-pod-error"},
			expectNoError:         true, // executeCleanup logs errors but doesn't return them
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake k8s client
			objects := make([]runtime.Object, len(tt.orphanedPods))
			for i := range tt.orphanedPods {
				objects[i] = &tt.orphanedPods[i]
			}
			k8sClient := fake.NewSimpleClientset(objects...)

			// Setup cleanup errors
			for _, errorName := range tt.simulateCleanupErrors {
				k8sClient.PrependReactor("delete", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					deleteAction := action.(k8stesting.DeleteAction)
					if deleteAction.GetName() == errorName {
						return true, nil, errors.New("cleanup failed")
					}
					return false, nil, nil
				})
			}

			// Create cleaner and test
			cleaner := NewCleaner(k8sClient, &mockICMSRequestGetter{}, nil, testNamespace)
			err := cleaner.executeCleanup(ctx, tt.orphanedPods)

			if tt.expectNoError {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// Test Run method
func TestCleaner_Run(t *testing.T) {
	tests := []struct {
		name                 string
		pods                 []corev1.Pod
		icmsRequests         []string
		expectedCleanupCount int
	}{
		{
			name:                 "no pods",
			pods:                 []corev1.Pod{},
			icmsRequests:         []string{},
			expectedCleanupCount: 0,
		},
		{
			name: "one orphaned pod - missing ICMSRequest",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "orphaned-pod",
						Namespace: testNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
								Kind:       "ICMSRequest",
								Name:       "sr-missing",
							},
						},
					},
				},
			},
			icmsRequests:         []string{},
			expectedCleanupCount: 1,
		},
		{
			name: "mixed pods - some orphaned, some not",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "orphaned-pod",
						Namespace: testNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
								Kind:       "ICMSRequest",
								Name:       "sr-missing",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod",
						Namespace: testNamespace,
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
								Kind:       "ICMSRequest",
								Name:       "sr-existing",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-without-owner",
						Namespace: testNamespace,
					},
				},
			},
			icmsRequests:         []string{"sr-existing"},
			expectedCleanupCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake k8s client with pods
			objects := make([]runtime.Object, len(tt.pods))
			for i := range tt.pods {
				objects[i] = &tt.pods[i]
			}
			k8sClient := fake.NewSimpleClientset(objects...)

			// Create mock ICMSRequest getter
			mockICMSGetter := &mockICMSRequestGetter{
				requests: make(map[string]*nvcav2beta1.ICMSRequest),
				errors:   make(map[string]error),
			}

			for _, req := range tt.icmsRequests {
				mockICMSGetter.requests[req] = &nvcav2beta1.ICMSRequest{
					ObjectMeta: metav1.ObjectMeta{
						Name:      req,
						Namespace: types.DefaultICMSRequestNamespace,
					},
				}
			}

			// Create cleaner and run
			cleaner := NewCleaner(k8sClient, mockICMSGetter, nil, testNamespace)
			err := cleaner.Run(ctx)

			assert.NoError(t, err)

			// Check how many pods remain
			remainingPods, err := k8sClient.CoreV1().Pods(testNamespace).List(ctx, metav1.ListOptions{})
			require.NoError(t, err)

			expectedRemaining := len(tt.pods) - tt.expectedCleanupCount
			assert.Equal(t, expectedRemaining, len(remainingPods.Items), "Unexpected number of pods remaining")
		})
	}
}

// Test Run method with list errors
func TestCleaner_Run_ListError(t *testing.T) {
	ctx := context.Background()

	// Create fake k8s client that fails on list
	k8sClient := fake.NewSimpleClientset()
	k8sClient.PrependReactor("list", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, errors.New("failed to list pods")
	})

	// Create cleaner and test
	cleaner := NewCleaner(k8sClient, &mockICMSRequestGetter{}, nil, testNamespace)
	err := cleaner.Run(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to collect orphaned pods")
}

// Test Run method with large number of pods
func TestCleaner_Run_LargeScale(t *testing.T) {
	ctx := context.Background()

	// Create many pods
	const numPods = 50
	pods := make([]runtime.Object, numPods)
	for i := 0; i < numPods; i++ {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("orphaned-pod-%d", i),
				Namespace: testNamespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
						Kind:       "ICMSRequest",
						Name:       fmt.Sprintf("sr-missing-%d", i),
					},
				},
			},
		}
		pods[i] = pod
	}

	// Create fake k8s client
	k8sClient := fake.NewSimpleClientset(pods...)

	// Create mock ICMSRequest getter (no requests exist)
	mockICMSGetter := &mockICMSRequestGetter{
		requests: make(map[string]*nvcav2beta1.ICMSRequest),
	}

	// Create cleaner and test
	cleaner := NewCleaner(k8sClient, mockICMSGetter, nil, testNamespace)
	err := cleaner.Run(ctx)

	assert.NoError(t, err)

	// Verify all pods were cleaned up
	remainingPods, err := k8sClient.CoreV1().Pods(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, len(remainingPods.Items))
}

// Test executeCleanup with high concurrency to detect data races
func TestCleaner_executeCleanup_DataRaceStress(t *testing.T) {
	ctx := context.Background()

	// Create a large number of orphaned pods to stress test concurrent cleanup
	const numPods = 100
	orphanedPods := make([]corev1.Pod, numPods)
	objects := make([]runtime.Object, numPods)

	for i := 0; i < numPods; i++ {
		pod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("stress-test-pod-%d", i),
				Namespace: testNamespace,
				// Mix of pods with and without finalizers to test different code paths
				Finalizers: func() []string {
					if i%3 == 0 {
						return []string{fmt.Sprintf("test-finalizer-%d", i)}
					}
					return nil
				}(),
			},
		}
		orphanedPods[i] = pod
		objects[i] = &pod
	}

	k8sClient := fake.NewSimpleClientset(objects...)

	// Simulate some random failures to test error handling under concurrency
	var deletionAttempts sync.Map
	k8sClient.PrependReactor("delete", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		deleteAction := action.(k8stesting.DeleteAction)
		podName := deleteAction.GetName()

		// Store each deletion attempt
		if _, loaded := deletionAttempts.LoadOrStore(podName, true); loaded {
			// Pod was already attempted to be deleted
			t.Errorf("Pod %s was attempted to be deleted multiple times - potential data race", podName)
		}

		// Fail 10% of deletions randomly to test error handling
		hash := 0
		for _, c := range podName {
			hash += int(c)
		}
		if hash%10 == 0 {
			return true, nil, errors.New("simulated deletion failure")
		}

		return false, nil, nil
	})

	cleaner := NewCleaner(k8sClient, &mockICMSRequestGetter{}, nil, testNamespace)
	err := cleaner.executeCleanup(ctx, orphanedPods)

	// Should always complete without error even if some deletions fail
	assert.NoError(t, err)

	// Verify that we attempted to delete each pod exactly once
	uniqueDeletions := 0
	deletionAttempts.Range(func(key, value interface{}) bool {
		uniqueDeletions++
		return true
	})

	// We should have attempted to delete all pods
	assert.Equal(t, numPods, uniqueDeletions, "Each pod should be deleted exactly once")
}

// Test parallel cleanup of pods with complex owner references
func TestCleaner_Run_ParallelComplexOwners(t *testing.T) {
	ctx := context.Background()

	const numPods = 200
	pods := make([]runtime.Object, numPods)

	// Create a mix of pods with different ownership patterns
	for i := 0; i < numPods; i++ {
		var ownerRefs []metav1.OwnerReference

		switch i % 4 {
		case 0:
			// ICMSRequest owner that doesn't exist
			ownerRefs = []metav1.OwnerReference{
				{
					APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
					Kind:       "ICMSRequest",
					Name:       fmt.Sprintf("sr-missing-%d", i),
				},
			}
		case 1:
			// Multiple owners including ICMSRequest
			ownerRefs = []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "some-replicaset",
				},
				{
					APIVersion: "nvca.nvcf.nvidia.io/v2beta1",
					Kind:       "ICMSRequest",
					Name:       fmt.Sprintf("sr-missing-%d", i),
				},
			}
		case 2:
			// No owner references
			ownerRefs = nil
		case 3:
			// Non-ICMSRequest owner only
			ownerRefs = []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "some-deployment",
				},
			}
		}

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:            fmt.Sprintf("complex-pod-%d", i),
				Namespace:       testNamespace,
				OwnerReferences: ownerRefs,
			},
		}
		pods[i] = pod
	}

	k8sClient := fake.NewSimpleClientset(pods...)

	// No ICMSRequests exist
	mockICMSGetter := &mockICMSRequestGetter{
		requests: make(map[string]*nvcav2beta1.ICMSRequest),
	}

	cleaner := NewCleaner(k8sClient, mockICMSGetter, nil, testNamespace)
	err := cleaner.Run(ctx)

	assert.NoError(t, err)

	// Verify that only pods with missing ICMSRequest owners were cleaned up
	// That's case 0 and 1, which is 50% of pods
	remainingPods, err := k8sClient.CoreV1().Pods(testNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	expectedRemaining := numPods / 2 // Cases 2 and 3 should remain
	assert.Equal(t, expectedRemaining, len(remainingPods.Items))
}

// Test conflict retry scenario
func TestCleaner_cleanupPod_ConflictRetry(t *testing.T) {
	ctx := context.Background()

	// Pod with finalizers
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-pod",
			Namespace:  testNamespace,
			Finalizers: []string{"test-finalizer"},
		},
	}

	k8sClient := fake.NewSimpleClientset(pod)

	// Simulate conflict on first update, success on second
	var updateAttempts int
	k8sClient.PrependReactor("update", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		updateAttempts++
		if updateAttempts == 1 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "", Resource: "pods"}, pod.Name, errors.New("conflict"))
		}
		return false, nil, nil // Let normal processing continue
	})

	cleaner := NewCleaner(k8sClient, &mockICMSRequestGetter{}, nil, testNamespace)
	err := cleaner.cleanupPod(ctx, pod)

	assert.NoError(t, err)
	assert.Equal(t, 2, updateAttempts, "Pod should have been retried on conflict")

	// Verify pod was deleted
	_, err = k8sClient.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
}

// Test resource deleted during retry
func TestCleaner_cleanupPod_ResourceDeletedDuringRetry(t *testing.T) {
	ctx := context.Background()

	// Pod with finalizers
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-pod",
			Namespace:  testNamespace,
			Finalizers: []string{"test-finalizer"},
		},
	}

	k8sClient := fake.NewSimpleClientset(pod)

	// Simulate resource being deleted by another process during retry
	var getAttempts int
	k8sClient.PrependReactor("get", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		getAttempts++
		if getAttempts > 1 {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, pod.Name)
		}
		return false, nil, nil // Let normal processing continue
	})

	// First update fails with conflict
	k8sClient.PrependReactor("update", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "", Resource: "pods"}, pod.Name, errors.New("conflict"))
	})

	cleaner := NewCleaner(k8sClient, &mockICMSRequestGetter{}, nil, testNamespace)
	err := cleaner.cleanupPod(ctx, pod)

	assert.NoError(t, err) // Should not error when resource is deleted during retry
	assert.Greater(t, getAttempts, 1, "Get should have been retried")
}
