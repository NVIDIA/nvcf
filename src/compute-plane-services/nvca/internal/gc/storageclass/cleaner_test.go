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

package storageclass

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
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

	cleaner := NewCleaner(k8sClient, icmsGetter, nil)

	assert.NotNil(t, cleaner)
	assert.Equal(t, k8sClient, cleaner.k8sClient)
	assert.Equal(t, icmsGetter, cleaner.icmsRequestGetter)
}

// Test Name method
func TestCleaner_Name(t *testing.T) {
	cleaner := &Cleaner{}
	assert.Equal(t, "StorageClassCleaner", cleaner.Name())
}

// Test collectOrphanedStorageClasses method
func TestCleaner_collectOrphanedStorageClasses(t *testing.T) {
	tests := []struct {
		name              string
		storageClasses    []storagev1.StorageClass
		namespaces        []string
		icmsRequests      map[string]*nvcav2beta1.ICMSRequest
		icmsRequestErrors map[string]error
		expectedOrphaned  int
		expectError       bool
		simulateListError bool
	}{
		{
			name:             "no storage classes",
			storageClasses:   []storagev1.StorageClass{},
			expectedOrphaned: 0,
			expectError:      false,
		},
		{
			name: "storage classes with wrong owner labels are ignored",
			storageClasses: []storagev1.StorageClass{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "wrong-owner-sc",
						Labels: map[string]string{
							storage.StorageRequestOwnerKey:     nvcav1new.ModelCacheRequest.Name(),
							storage.StorageRequestNamespaceKey: "some-namespace",
						},
					},
				},
			},
			namespaces: []string{"some-namespace"},
			icmsRequests: map[string]*nvcav2beta1.ICMSRequest{
				"some-namespace": {
					ObjectMeta: metav1.ObjectMeta{
						Name:      "some-namespace",
						Namespace: types.DefaultICMSRequestNamespace,
					},
				},
			},
			expectedOrphaned: 0,
			expectError:      false,
		},
		{
			name: "collect orphaned storage classes - namespace missing",
			storageClasses: []storagev1.StorageClass{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "orphaned-sc",
						Labels: map[string]string{
							storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
							storage.StorageRequestNamespaceKey: "missing-namespace",
						},
					},
				},
			},
			namespaces: []string{},
			icmsRequests: map[string]*nvcav2beta1.ICMSRequest{
				"missing-namespace": {
					ObjectMeta: metav1.ObjectMeta{
						Name:      "missing-namespace",
						Namespace: types.DefaultICMSRequestNamespace,
					},
				},
			},
			expectedOrphaned: 1,
			expectError:      false,
		},
		{
			name: "collect orphaned storage classes - ICMSRequest missing",
			storageClasses: []storagev1.StorageClass{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "orphaned-sc",
						Labels: map[string]string{
							storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
							storage.StorageRequestNamespaceKey: "existing-namespace",
						},
					},
				},
			},
			namespaces:       []string{"existing-namespace"},
			icmsRequests:     map[string]*nvcav2beta1.ICMSRequest{},
			expectedOrphaned: 1,
			expectError:      false,
		},
		{
			name:              "error listing storage classes",
			storageClasses:    []storagev1.StorageClass{},
			simulateListError: true,
			expectError:       true,
		},
		{
			name: "error checking ICMSRequest but not not-found",
			storageClasses: []storagev1.StorageClass{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-sc",
						Labels: map[string]string{
							storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
							storage.StorageRequestNamespaceKey: "existing-namespace",
						},
					},
				},
			},
			namespaces: []string{"existing-namespace"},
			icmsRequestErrors: map[string]error{
				"existing-namespace": errors.New("internal server error"),
			},
			expectedOrphaned: 0, // Should not be considered orphaned due to error
			expectError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake k8s client
			objects := make([]runtime.Object, len(tt.storageClasses))
			for i, sc := range tt.storageClasses {
				objects[i] = &sc
			}
			k8sClient := fake.NewSimpleClientset(objects...)

			// Simulate list error if needed
			if tt.simulateListError {
				k8sClient.PrependReactor("list", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("failed to list storage classes")
				})
			}

			// Create namespaces
			for _, ns := range tt.namespaces {
				namespace := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: ns,
					},
				}
				_, err := k8sClient.CoreV1().Namespaces().Create(ctx, namespace, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			// Create mock ICMSRequest getter
			mockICMSGetter := &mockICMSRequestGetter{
				requests: tt.icmsRequests,
				errors:   tt.icmsRequestErrors,
			}

			// Create cleaner and test
			cleaner := NewCleaner(k8sClient, mockICMSGetter, nil)
			orphaned, err := cleaner.collectOrphanedStorageClasses(ctx)

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
func TestCleaner_isOrphaned_EdgeCases(t *testing.T) {
	tests := []struct {
		name                  string
		storageClass          *storagev1.StorageClass
		namespaceExists       bool
		namespaceCheckError   error
		icmsRequestExists     bool
		icmsRequestCheckError error
		expected              bool
	}{
		{
			name: "namespace check error - not orphaned",
			storageClass: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-sc",
					Labels: map[string]string{
						storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
						storage.StorageRequestNamespaceKey: "test-namespace",
					},
				},
			},
			namespaceExists:     false,
			namespaceCheckError: errors.New("internal server error"),
			expected:            false,
		},
		{
			name: "ICMSRequest check error - not orphaned",
			storageClass: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-sc",
					Labels: map[string]string{
						storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
						storage.StorageRequestNamespaceKey: "test-namespace",
					},
				},
			},
			namespaceExists:       true,
			icmsRequestExists:     false,
			icmsRequestCheckError: errors.New("internal server error"),
			expected:              false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake k8s client
			k8sClient := fake.NewSimpleClientset()

			// Create namespace if it should exist
			if tt.namespaceExists {
				nsName := tt.storageClass.Labels[storage.StorageRequestNamespaceKey]
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: nsName,
					},
				}
				_, err := k8sClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			// Setup namespace check error
			if tt.namespaceCheckError != nil {
				k8sClient.PrependReactor("get", "namespaces", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, tt.namespaceCheckError
				})
			}

			// Create mock ICMSRequest getter
			mockICMSGetter := &mockICMSRequestGetter{
				requests: make(map[string]*nvcav2beta1.ICMSRequest),
				errors:   make(map[string]error),
			}

			nsName := tt.storageClass.Labels[storage.StorageRequestNamespaceKey]
			if tt.icmsRequestExists {
				mockICMSGetter.requests[nsName] = &nvcav2beta1.ICMSRequest{
					ObjectMeta: metav1.ObjectMeta{
						Name:      nsName,
						Namespace: types.DefaultICMSRequestNamespace,
					},
				}
			}

			if tt.icmsRequestCheckError != nil {
				mockICMSGetter.errors[nsName] = tt.icmsRequestCheckError
			}

			// Create cleaner and test
			cleaner := NewCleaner(k8sClient, mockICMSGetter, nil)
			result := cleaner.isOrphaned(ctx, tt.storageClass)

			assert.Equal(t, tt.expected, result)
		})
	}
}

// Test cleanupStorageClass edge cases
func TestCleaner_cleanupStorageClass_EdgeCases(t *testing.T) {
	tests := []struct {
		name                   string
		storageClass           *storagev1.StorageClass
		simulateUpdateError    bool
		simulateDeleteError    bool
		simulateAlreadyDeleted bool
		expectError            bool
	}{
		{
			name: "finalizer update error",
			storageClass: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-sc",
					Finalizers: []string{"test-finalizer"},
				},
			},
			simulateUpdateError: true,
			expectError:         true,
		},
		{
			name: "delete error",
			storageClass: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-sc",
				},
			},
			simulateDeleteError: true,
			expectError:         true,
		},
		{
			name: "already deleted during cleanup",
			storageClass: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-sc",
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
			k8sClient := fake.NewSimpleClientset(tt.storageClass)

			// Setup error simulation
			if tt.simulateUpdateError {
				k8sClient.PrependReactor("update", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("update failed")
				})
			}

			if tt.simulateDeleteError {
				k8sClient.PrependReactor("delete", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, errors.New("delete failed")
				})
			}

			if tt.simulateAlreadyDeleted {
				k8sClient.PrependReactor("delete", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "storage.k8s.io", Resource: "storageclasses"}, tt.storageClass.Name)
				})
			}

			// Create mock ICMSRequest getter
			mockICMSGetter := &mockICMSRequestGetter{
				requests: make(map[string]*nvcav2beta1.ICMSRequest),
			}

			// Create cleaner and test
			cleaner := NewCleaner(k8sClient, mockICMSGetter, nil)
			err := cleaner.cleanupStorageClass(ctx, tt.storageClass)

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
		name                   string
		orphanedStorageClasses []storagev1.StorageClass
		simulateCleanupErrors  []string // Storage class names that should fail cleanup
		expectNoError          bool
	}{
		{
			name:                   "no orphaned storage classes",
			orphanedStorageClasses: []storagev1.StorageClass{},
			expectNoError:          true,
		},
		{
			name: "successful cleanup of multiple storage classes",
			orphanedStorageClasses: []storagev1.StorageClass{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "orphaned-sc-1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "orphaned-sc-2",
					},
				},
			},
			expectNoError: true,
		},
		{
			name: "cleanup with some errors",
			orphanedStorageClasses: []storagev1.StorageClass{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "orphaned-sc-success",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "orphaned-sc-error",
					},
				},
			},
			simulateCleanupErrors: []string{"orphaned-sc-error"},
			expectNoError:         true, // executeCleanup logs errors but doesn't return them
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake k8s client
			objects := make([]runtime.Object, len(tt.orphanedStorageClasses))
			for i, sc := range tt.orphanedStorageClasses {
				objects[i] = &sc
			}
			k8sClient := fake.NewSimpleClientset(objects...)

			// Setup cleanup errors
			for _, errorName := range tt.simulateCleanupErrors {
				k8sClient.PrependReactor("delete", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
					deleteAction := action.(k8stesting.DeleteAction)
					if deleteAction.GetName() == errorName {
						return true, nil, errors.New("cleanup failed")
					}
					return false, nil, nil
				})
			}

			// Create mock ICMSRequest getter
			mockICMSGetter := &mockICMSRequestGetter{
				requests: make(map[string]*nvcav2beta1.ICMSRequest),
			}

			// Create cleaner and test
			cleaner := NewCleaner(k8sClient, mockICMSGetter, nil)
			err := cleaner.executeCleanup(ctx, tt.orphanedStorageClasses)

			if tt.expectNoError {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// Test Run method with list errors
func TestCleaner_Run_ListError(t *testing.T) {
	ctx := context.Background()

	// Create fake k8s client that fails on list
	k8sClient := fake.NewSimpleClientset()
	k8sClient.PrependReactor("list", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, errors.New("failed to list storage classes")
	})

	// Create mock ICMSRequest getter
	mockICMSGetter := &mockICMSRequestGetter{
		requests: make(map[string]*nvcav2beta1.ICMSRequest),
	}

	// Create cleaner and test
	cleaner := NewCleaner(k8sClient, mockICMSGetter, nil)
	err := cleaner.Run(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to collect orphaned storage classes")
}

// Test Run method with large number of storage classes
func TestCleaner_Run_LargeScale(t *testing.T) {
	ctx := context.Background()

	// Create many storage classes
	const numStorageClasses = 50
	storageClasses := make([]runtime.Object, numStorageClasses)
	for i := 0; i < numStorageClasses; i++ {
		sc := &storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("orphaned-sc-%d", i),
				Labels: map[string]string{
					storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
					storage.StorageRequestNamespaceKey: fmt.Sprintf("missing-namespace-%d", i),
				},
			},
		}
		storageClasses[i] = sc
	}

	// Create fake k8s client
	k8sClient := fake.NewSimpleClientset(storageClasses...)

	// Create mock ICMSRequest getter
	mockICMSGetter := &mockICMSRequestGetter{
		requests: make(map[string]*nvcav2beta1.ICMSRequest),
	}

	// Create cleaner and test
	cleaner := NewCleaner(k8sClient, mockICMSGetter, nil)
	err := cleaner.Run(ctx)

	assert.NoError(t, err)

	// Verify all storage classes were cleaned up
	remainingSCs, err := k8sClient.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, len(remainingSCs.Items))
}

func TestCleaner_isOrphaned(t *testing.T) {
	tests := []struct {
		name              string
		storageClass      *storagev1.StorageClass
		namespaceExists   bool
		icmsRequestExists bool
		expected          bool
	}{
		{
			name: "orphaned - namespace does not exist",
			storageClass: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-sc",
					Labels: map[string]string{
						storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
						storage.StorageRequestNamespaceKey: "missing-namespace",
					},
				},
			},
			namespaceExists:   false,
			icmsRequestExists: true,
			expected:          true,
		},
		{
			name: "orphaned - ICMSRequest does not exist in nvcf-backend namespace",
			storageClass: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-sc",
					Labels: map[string]string{
						storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
						storage.StorageRequestNamespaceKey: "sr-1f2813f8-9ec4-47eb-9c7d-daf96283ccaf",
					},
				},
			},
			namespaceExists:   true,
			icmsRequestExists: false,
			expected:          true,
		},
		{
			name: "not orphaned - both namespace and ICMSRequest exist",
			storageClass: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-sc",
					Labels: map[string]string{
						storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
						storage.StorageRequestNamespaceKey: "existing-namespace",
					},
				},
			},
			namespaceExists:   true,
			icmsRequestExists: true,
			expected:          false,
		},
		{
			name: "not orphaned - missing required labels",
			storageClass: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-sc",
					Labels: map[string]string{
						"some-other-label": "value",
					},
				},
			},
			namespaceExists:   true,
			icmsRequestExists: true,
			expected:          false,
		},
		{
			name: "not orphaned - wrong storage request type",
			storageClass: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-sc",
					Labels: map[string]string{
						storage.StorageRequestOwnerKey:     nvcav1new.ModelCacheRequest.Name(),
						storage.StorageRequestNamespaceKey: "some-namespace",
					},
				},
			},
			namespaceExists:   true,
			icmsRequestExists: true,
			expected:          false,
		},
		{
			name: "not orphaned - no labels",
			storageClass: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-sc",
				},
			},
			namespaceExists:   true,
			icmsRequestExists: true,
			expected:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake k8s client
			k8sClient := fake.NewSimpleClientset()

			// Create namespace if it should exist
			if tt.namespaceExists {
				nsName := tt.storageClass.Labels[storage.StorageRequestNamespaceKey]
				if nsName != "" {
					ns := &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: nsName,
						},
					}
					_, err := k8sClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
					require.NoError(t, err)
				}
			}

			// Create mock ICMSRequest getter
			mockICMSGetter := &mockICMSRequestGetter{
				requests: make(map[string]*nvcav2beta1.ICMSRequest),
				errors:   make(map[string]error),
			}

			if tt.icmsRequestExists {
				nsName := tt.storageClass.Labels[storage.StorageRequestNamespaceKey]
				if nsName != "" {
					mockICMSGetter.requests[nsName] = &nvcav2beta1.ICMSRequest{
						ObjectMeta: metav1.ObjectMeta{
							Name:      nsName,
							Namespace: types.DefaultICMSRequestNamespace,
						},
					}
				}
			}

			// Create cleaner and test
			cleaner := NewCleaner(k8sClient, mockICMSGetter, nil)
			result := cleaner.isOrphaned(ctx, tt.storageClass)

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCleaner_cleanupStorageClass(t *testing.T) {
	tests := []struct {
		name                   string
		storageClass           *storagev1.StorageClass
		expectError            bool
		expectDeletion         bool
		expectFinalizerRemoval bool
	}{
		{
			name: "successful cleanup without finalizers",
			storageClass: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-sc",
				},
			},
			expectError:            false,
			expectDeletion:         true,
			expectFinalizerRemoval: false,
		},
		{
			name: "successful cleanup with finalizers",
			storageClass: &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-sc-with-finalizers",
					Finalizers: []string{
						"test-finalizer",
					},
				},
			},
			expectError:            false,
			expectDeletion:         true,
			expectFinalizerRemoval: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake k8s client with the storage class
			k8sClient := fake.NewSimpleClientset(tt.storageClass)

			// Create mock ICMSRequest getter (not used in this test)
			mockICMSGetter := &mockICMSRequestGetter{
				requests: make(map[string]*nvcav2beta1.ICMSRequest),
				errors:   make(map[string]error),
			}

			// Create cleaner and test
			cleaner := NewCleaner(k8sClient, mockICMSGetter, nil)
			err := cleaner.cleanupStorageClass(ctx, tt.storageClass)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Check if storage class was deleted
			if tt.expectDeletion {
				_, err := k8sClient.StorageV1().StorageClasses().Get(ctx, tt.storageClass.Name, metav1.GetOptions{})
				assert.True(t, apierrors.IsNotFound(err), "Storage class should be deleted")
			}
		})
	}
}

func TestCleaner_Run(t *testing.T) {
	tests := []struct {
		name                 string
		storageClasses       []storagev1.StorageClass
		namespaces           []string
		icmsRequests         []string
		expectedCleanupCount int
	}{
		{
			name:                 "no storage classes",
			storageClasses:       []storagev1.StorageClass{},
			namespaces:           []string{},
			icmsRequests:         []string{},
			expectedCleanupCount: 0,
		},
		{
			name: "one orphaned storage class - missing namespace",
			storageClasses: []storagev1.StorageClass{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "orphaned-sc",
						Labels: map[string]string{
							storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
							storage.StorageRequestNamespaceKey: "missing-namespace",
						},
					},
				},
			},
			namespaces:           []string{},
			icmsRequests:         []string{"missing-namespace"},
			expectedCleanupCount: 1,
		},
		{
			name: "one orphaned storage class - missing ICMSRequest",
			storageClasses: []storagev1.StorageClass{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "orphaned-sc",
						Labels: map[string]string{
							storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
							storage.StorageRequestNamespaceKey: "existing-namespace",
						},
					},
				},
			},
			namespaces:           []string{"existing-namespace"},
			icmsRequests:         []string{},
			expectedCleanupCount: 1,
		},
		{
			name: "mixed storage classes - some orphaned, some not",
			storageClasses: []storagev1.StorageClass{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "orphaned-sc",
						Labels: map[string]string{
							storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
							storage.StorageRequestNamespaceKey: "missing-namespace",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "valid-sc",
						Labels: map[string]string{
							storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
							storage.StorageRequestNamespaceKey: "existing-namespace",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "unrelated-sc",
						Labels: map[string]string{
							"some-other-label": "value",
						},
					},
				},
			},
			namespaces:           []string{"existing-namespace"},
			icmsRequests:         []string{"existing-namespace"},
			expectedCleanupCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake k8s client with storage classes
			objects := make([]runtime.Object, len(tt.storageClasses))
			for i, sc := range tt.storageClasses {
				objects[i] = &sc
			}
			k8sClient := fake.NewSimpleClientset(objects...)

			// Create namespaces
			for _, ns := range tt.namespaces {
				namespace := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: ns,
					},
				}
				_, err := k8sClient.CoreV1().Namespaces().Create(ctx, namespace, metav1.CreateOptions{})
				require.NoError(t, err)
			}

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
			cleaner := NewCleaner(k8sClient, mockICMSGetter, nil)
			err := cleaner.Run(ctx)

			assert.NoError(t, err)

			// Check how many storage classes remain
			remainingSCs, err := k8sClient.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
			require.NoError(t, err)

			expectedRemaining := len(tt.storageClasses) - tt.expectedCleanupCount
			assert.Equal(t, expectedRemaining, len(remainingSCs.Items), "Unexpected number of storage classes remaining")
		})
	}
}

func TestCleaner_cleanupStorageClass_ConflictRetry(t *testing.T) {
	ctx := context.Background()

	// Storage class with finalizers
	sc := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-sc",
			Finalizers: []string{"test-finalizer"},
		},
	}

	k8sClient := fake.NewSimpleClientset(sc)

	// Simulate conflict on first update, success on second
	var updateAttempts int
	k8sClient.PrependReactor("update", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		updateAttempts++
		if updateAttempts == 1 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "storage.k8s.io", Resource: "storageclasses"}, sc.Name, errors.New("conflict"))
		}
		return false, nil, nil // Let normal processing continue
	})

	mockICMSGetter := &mockICMSRequestGetter{
		requests: make(map[string]*nvcav2beta1.ICMSRequest),
	}

	cleaner := NewCleaner(k8sClient, mockICMSGetter, nil)
	err := cleaner.cleanupStorageClass(ctx, sc)

	assert.NoError(t, err)
	assert.Equal(t, 2, updateAttempts, "StorageClass should have been retried on conflict")

	// Verify storage class was deleted
	_, err = k8sClient.StorageV1().StorageClasses().Get(ctx, sc.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestCleaner_cleanupStorageClass_ResourceDeletedDuringRetry(t *testing.T) {
	ctx := context.Background()

	// Storage class with finalizers
	sc := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-sc",
			Finalizers: []string{"test-finalizer"},
		},
	}

	k8sClient := fake.NewSimpleClientset(sc)

	// Simulate resource being deleted by another process during retry
	var getAttempts int
	k8sClient.PrependReactor("get", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		getAttempts++
		if getAttempts > 1 {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "storage.k8s.io", Resource: "storageclasses"}, sc.Name)
		}
		return false, nil, nil // Let normal processing continue
	})

	// First update fails with conflict
	k8sClient.PrependReactor("update", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "storage.k8s.io", Resource: "storageclasses"}, sc.Name, errors.New("conflict"))
	})

	mockICMSGetter := &mockICMSRequestGetter{
		requests: make(map[string]*nvcav2beta1.ICMSRequest),
	}

	cleaner := NewCleaner(k8sClient, mockICMSGetter, nil)
	err := cleaner.cleanupStorageClass(ctx, sc)

	assert.NoError(t, err) // Should not error when resource is deleted during retry
	assert.Greater(t, getAttempts, 1, "Get should have been retried")
}

// Test executeCleanup with high concurrency to detect data races
func TestCleaner_executeCleanup_DataRaceStress(t *testing.T) {
	ctx := context.Background()

	// Create a large number of orphaned storage classes to stress test concurrent cleanup
	const numStorageClasses = 100
	orphanedStorageClasses := make([]storagev1.StorageClass, numStorageClasses)
	objects := make([]runtime.Object, numStorageClasses)

	for i := 0; i < numStorageClasses; i++ {
		sc := storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("stress-test-sc-%d", i),
				Labels: map[string]string{
					storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
					storage.StorageRequestNamespaceKey: fmt.Sprintf("ns-%d", i),
				},
				// Mix of storage classes with and without finalizers to test different code paths
				Finalizers: func() []string {
					if i%3 == 0 {
						return []string{fmt.Sprintf("test-finalizer-%d", i)}
					}
					return nil
				}(),
			},
		}
		orphanedStorageClasses[i] = sc
		objects[i] = &sc
	}

	k8sClient := fake.NewSimpleClientset(objects...)

	// Simulate some random failures to test error handling under concurrency
	var deletionAttempts sync.Map
	k8sClient.PrependReactor("delete", "storageclasses", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		deleteAction := action.(k8stesting.DeleteAction)
		scName := deleteAction.GetName()

		// Store each deletion attempt
		if _, loaded := deletionAttempts.LoadOrStore(scName, true); loaded {
			// Storage class was already attempted to be deleted
			t.Errorf("StorageClass %s was attempted to be deleted multiple times - potential data race", scName)
		}

		// Fail 10% of deletions randomly to test error handling
		hash := 0
		for _, c := range scName {
			hash += int(c)
		}
		if hash%10 == 0 {
			return true, nil, errors.New("simulated deletion failure")
		}

		return false, nil, nil
	})

	cleaner := NewCleaner(k8sClient, &mockICMSRequestGetter{}, nil)
	err := cleaner.executeCleanup(ctx, orphanedStorageClasses)

	// Should always complete without error even if some deletions fail
	assert.NoError(t, err)

	// Verify that we attempted to delete each storage class exactly once
	uniqueDeletions := 0
	deletionAttempts.Range(func(key, value interface{}) bool {
		uniqueDeletions++
		return true
	})

	// We should have attempted to delete all storage classes
	assert.Equal(t, numStorageClasses, uniqueDeletions, "Each storage class should be deleted exactly once")
}

// Test parallel cleanup of storage classes with complex patterns
func TestCleaner_Run_ParallelComplexOwners(t *testing.T) {
	ctx := context.Background()

	const numStorageClasses = 200
	objects := make([]runtime.Object, 0)
	namespaces := make(map[string]bool)

	// Create a mix of storage classes with different ownership patterns
	for i := 0; i < numStorageClasses; i++ {
		var labels map[string]string

		switch i % 4 {
		case 0:
			// Orphaned - namespace doesn't exist, correct owner
			labels = map[string]string{
				storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
				storage.StorageRequestNamespaceKey: fmt.Sprintf("missing-ns-%d", i),
			}
		case 1:
			// Orphaned - namespace exists but ICMSRequest doesn't
			nsName := fmt.Sprintf("existing-ns-%d", i)
			namespaces[nsName] = true
			labels = map[string]string{
				storage.StorageRequestOwnerKey:     nvcav1new.SharedStorageRequest.Name(),
				storage.StorageRequestNamespaceKey: nsName,
			}
		case 2:
			// Not orphaned - wrong owner type
			labels = map[string]string{
				storage.StorageRequestOwnerKey:     nvcav1new.ModelCacheRequest.Name(),
				storage.StorageRequestNamespaceKey: "some-namespace",
			}
		case 3:
			// Not orphaned - missing required labels
			labels = map[string]string{
				"some-other-label": "value",
			}
		}

		sc := &storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:   fmt.Sprintf("complex-sc-%d", i),
				Labels: labels,
			},
		}
		objects = append(objects, sc)
	}

	k8sClient := fake.NewSimpleClientset(objects...)

	// Create namespaces
	for nsName := range namespaces {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: nsName,
			},
		}
		_, err := k8sClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	// No ICMSRequests exist
	mockICMSGetter := &mockICMSRequestGetter{
		requests: make(map[string]*nvcav2beta1.ICMSRequest),
	}

	cleaner := NewCleaner(k8sClient, mockICMSGetter, nil)
	err := cleaner.Run(ctx)

	assert.NoError(t, err)

	// Verify that only storage classes with correct owner and missing namespace/ICMSRequest were cleaned up
	// That's case 0 and 1, which is 50% of storage classes
	remainingSCs, err := k8sClient.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	expectedRemaining := numStorageClasses / 2 // Cases 2 and 3 should remain
	assert.Equal(t, expectedRemaining, len(remainingSCs.Items))
}
