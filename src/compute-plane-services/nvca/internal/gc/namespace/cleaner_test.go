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

package namespace

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
	bartfake "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// mockICMSRequestGetter implements ICMSRequestGetter for testing purposes.
type mockICMSRequestGetter struct {
	requests map[string]*nvcav2beta1.ICMSRequest
	errors   map[string]error
}

func (m *mockICMSRequestGetter) GetICMSRequest(_ context.Context, name string) (*nvcav2beta1.ICMSRequest, error) {
	if err, ok := m.errors[name]; ok {
		return nil, err
	}
	if req, ok := m.requests[name]; ok {
		return req, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: "nvca.nvcf.nvidia.io", Resource: "icmsrequests"}, name)
}

func TestNewCleaner(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	nvcaClient := bartfake.NewSimpleClientset()
	icmsGetter := &mockICMSRequestGetter{}

	cleaner := NewCleaner(k8sClient, nvcaClient, icmsGetter, nil)

	assert.NotNil(t, cleaner)
	assert.Equal(t, k8sClient, cleaner.k8sClient)
	assert.Equal(t, nvcaClient, cleaner.nvcaClient)
	assert.Equal(t, icmsGetter, cleaner.icmsRequestGetter)
}

func TestCleaner_Name(t *testing.T) {
	c := &Cleaner{}
	assert.Equal(t, "NamespaceCleaner", c.Name())
}

func TestCleaner_isOrphaned(t *testing.T) {
	tests := []struct {
		name              string
		namespaceObj      *corev1.Namespace
		icmsRequestExists bool
		expected          bool
	}{
		{
			name: "orphaned - ICMSRequest missing",
			namespaceObj: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "sr-missing",
					Labels: map[string]string{"nvca.nvcf.nvidia.io/workload-instance-type": "miniservice"},
				},
			},
			icmsRequestExists: false,
			expected:          true,
		},
		{
			name: "not orphaned - ICMSRequest exists",
			namespaceObj: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "sr-existing",
					Labels: map[string]string{"nvca.nvcf.nvidia.io/workload-instance-type": "miniservice"},
				},
			},
			icmsRequestExists: true,
			expected:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			k8sClient := fake.NewSimpleClientset()
			nvcaClient := bartfake.NewSimpleClientset()

			icmsGetter := &mockICMSRequestGetter{
				requests: map[string]*nvcav2beta1.ICMSRequest{},
				errors:   map[string]error{},
			}
			if tt.icmsRequestExists {
				icmsGetter.requests[tt.namespaceObj.Name] = &nvcav2beta1.ICMSRequest{ObjectMeta: metav1.ObjectMeta{Name: tt.namespaceObj.Name, Namespace: types.DefaultICMSRequestNamespace}}
			}

			cleaner := NewCleaner(k8sClient, nvcaClient, icmsGetter, nil)

			assert.Equal(t, tt.expected, cleaner.isOrphaned(ctx, tt.namespaceObj))
		})
	}
}

func TestCleaner_collectOrphanedNamespaces(t *testing.T) {
	ctx := context.Background()

	// Prepare namespaces
	nsOrphan := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns-orphan", Labels: map[string]string{"nvca.nvcf.nvidia.io/workload-instance-type": "miniservice"}}}
	nsValid := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns-valid", Labels: map[string]string{"nvca.nvcf.nvidia.io/workload-instance-type": "miniservice"}}}

	// StorageRequest inside nsValid (to be cleaned later)
	// Create fake clients
	k8sClient := fake.NewSimpleClientset(nsOrphan, nsValid)
	nvcaClient := bartfake.NewSimpleClientset()

	icmsGetter := &mockICMSRequestGetter{requests: map[string]*nvcav2beta1.ICMSRequest{"ns-valid": {ObjectMeta: metav1.ObjectMeta{Name: "ns-valid", Namespace: types.DefaultICMSRequestNamespace}}}}

	cleaner := NewCleaner(k8sClient, nvcaClient, icmsGetter, nil)

	orphaned, err := cleaner.collectOrphanedNamespaces(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, len(orphaned))
	assert.Equal(t, "ns-orphan", orphaned[0].Name)
}

func TestCleaner_collectOrphanedNamespaces_ListError(t *testing.T) {
	ctx := context.Background()

	k8sClient := fake.NewSimpleClientset()
	nvcaClient := bartfake.NewSimpleClientset()

	// Simulate list error
	k8sClient.Fake.PrependReactor("list", "namespaces", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, errors.New("list failed")
	})

	cleaner := NewCleaner(k8sClient, nvcaClient, &mockICMSRequestGetter{}, nil)

	_, err := cleaner.collectOrphanedNamespaces(ctx)
	assert.Error(t, err)
}

func TestCleaner_cleanupNamespace(t *testing.T) {
	ctx := context.Background()

	// Setup namespace, StorageRequest, and PVC with finalizers
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-ns"}}
	st := &nvcav2beta1.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "sr",
			Namespace:  "tenant-ns",
			Finalizers: []string{"test-finalizer"},
		},
		Spec: nvcav2beta1.StorageRequestSpec{
			Type:             nvcav2beta1.SharedStorageRequest,
			RequestName:      "sr",
			RequestNamespace: types.DefaultICMSRequestNamespace,
		},
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-pvc",
			Namespace:  "tenant-ns",
			Finalizers: []string{"test-pvc-finalizer"},
		},
	}

	k8sClient := fake.NewSimpleClientset(ns, pvc)
	nvcaClient := bartfake.NewSimpleClientset(st)

	cleaner := NewCleaner(k8sClient, nvcaClient, &mockICMSRequestGetter{}, nil)

	err := cleaner.cleanupNamespace(ctx, ns)
	assert.NoError(t, err)

	// Verify StorageRequest deleted
	_, err = nvcaClient.NvcaV2beta1().StorageRequests(st.Namespace).Get(ctx, st.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))

	// Verify PVC deleted
	_, err = k8sClient.CoreV1().PersistentVolumeClaims(pvc.Namespace).Get(ctx, pvc.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))

	// Verify Namespace deletion initiated
	_, err = k8sClient.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestCleaner_cleanupNamespace_PVCListError(t *testing.T) {
	ctx := context.Background()

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-ns"}}
	k8sClient := fake.NewSimpleClientset(ns)
	nvcaClient := bartfake.NewSimpleClientset()

	// Simulate PVC list error
	k8sClient.Fake.PrependReactor("list", "persistentvolumeclaims", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, errors.New("failed to list PVCs")
	})

	cleaner := NewCleaner(k8sClient, nvcaClient, &mockICMSRequestGetter{}, nil)

	err := cleaner.cleanupNamespace(ctx, ns)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list PVCs")
}

func TestCleaner_cleanupNamespace_NoPVCs(t *testing.T) {
	ctx := context.Background()

	// Setup namespace and StorageRequest without any PVCs
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-ns"}}
	st := &nvcav2beta1.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sr",
			Namespace: "tenant-ns",
		},
		Spec: nvcav2beta1.StorageRequestSpec{
			Type:             nvcav2beta1.SharedStorageRequest,
			RequestName:      "sr",
			RequestNamespace: types.DefaultICMSRequestNamespace,
		},
	}

	k8sClient := fake.NewSimpleClientset(ns)
	nvcaClient := bartfake.NewSimpleClientset(st)

	cleaner := NewCleaner(k8sClient, nvcaClient, &mockICMSRequestGetter{}, nil)

	err := cleaner.cleanupNamespace(ctx, ns)
	assert.NoError(t, err)

	// Verify StorageRequest deleted
	_, err = nvcaClient.NvcaV2beta1().StorageRequests(st.Namespace).Get(ctx, st.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))

	// Verify Namespace deletion initiated
	_, err = k8sClient.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestCleaner_cleanupNamespace_ConflictRetry(t *testing.T) {
	ctx := context.Background()

	// Setup namespace, StorageRequest, and PVC with finalizers
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-ns"}}
	st := &nvcav2beta1.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "sr",
			Namespace:  "tenant-ns",
			Finalizers: []string{"test-finalizer"},
		},
		Spec: nvcav2beta1.StorageRequestSpec{
			Type:             nvcav2beta1.SharedStorageRequest,
			RequestName:      "sr",
			RequestNamespace: types.DefaultICMSRequestNamespace,
		},
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-pvc",
			Namespace:  "tenant-ns",
			Finalizers: []string{"test-pvc-finalizer"},
		},
	}

	k8sClient := fake.NewSimpleClientset(ns, pvc)
	nvcaClient := bartfake.NewSimpleClientset(st)

	// Simulate conflict on first update, success on second
	var updateAttempts int
	nvcaClient.Fake.PrependReactor("update", "storagerequests", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		updateAttempts++
		if updateAttempts == 1 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "nvca.nvcf.nvidia.io", Resource: "storagerequests"}, st.Name, errors.New("conflict"))
		}
		return false, nil, nil // Let normal processing continue
	})

	var pvcUpdateAttempts int
	k8sClient.Fake.PrependReactor("update", "persistentvolumeclaims", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		pvcUpdateAttempts++
		if pvcUpdateAttempts == 1 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "", Resource: "persistentvolumeclaims"}, pvc.Name, errors.New("conflict"))
		}
		return false, nil, nil // Let normal processing continue
	})

	cleaner := NewCleaner(k8sClient, nvcaClient, &mockICMSRequestGetter{}, nil)

	err := cleaner.cleanupNamespace(ctx, ns)
	assert.NoError(t, err)

	// Verify retry happened for both StorageRequest and PVC
	assert.Equal(t, 2, updateAttempts, "StorageRequest should have been retried on conflict")
	assert.Equal(t, 2, pvcUpdateAttempts, "PVC should have been retried on conflict")

	// Verify resources deleted
	_, err = nvcaClient.NvcaV2beta1().StorageRequests(st.Namespace).Get(ctx, st.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))

	_, err = k8sClient.CoreV1().PersistentVolumeClaims(pvc.Namespace).Get(ctx, pvc.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestCleaner_cleanupNamespace_ResourceDeletedDuringRetry(t *testing.T) {
	ctx := context.Background()

	// Setup namespace and StorageRequest with finalizers
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-ns"}}
	st := &nvcav2beta1.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "sr",
			Namespace:  "tenant-ns",
			Finalizers: []string{"test-finalizer"},
		},
		Spec: nvcav2beta1.StorageRequestSpec{
			Type:             nvcav2beta1.SharedStorageRequest,
			RequestName:      "sr",
			RequestNamespace: types.DefaultICMSRequestNamespace,
		},
	}

	k8sClient := fake.NewSimpleClientset(ns)
	nvcaClient := bartfake.NewSimpleClientset(st)

	// Simulate resource being deleted by another process during retry
	var getAttempts int
	nvcaClient.Fake.PrependReactor("get", "storagerequests", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		getAttempts++
		if getAttempts > 1 {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "nvca.nvcf.nvidia.io", Resource: "storagerequests"}, st.Name)
		}
		return false, nil, nil // Let normal processing continue
	})

	// First update fails with conflict
	nvcaClient.Fake.PrependReactor("update", "storagerequests", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "nvca.nvcf.nvidia.io", Resource: "storagerequests"}, st.Name, errors.New("conflict"))
	})

	cleaner := NewCleaner(k8sClient, nvcaClient, &mockICMSRequestGetter{}, nil)

	err := cleaner.cleanupNamespace(ctx, ns)
	assert.NoError(t, err) // Should not error when resource is deleted during retry

	// Verify retry was attempted
	assert.Greater(t, getAttempts, 1, "Get should have been retried")
}

// Test executeCleanup with high concurrency to detect data races
func TestCleaner_executeCleanup_DataRaceStress(t *testing.T) {
	ctx := context.Background()

	// Create a large number of orphaned namespaces to stress test concurrent cleanup
	const numNamespaces = 100
	orphanedNamespaces := make([]corev1.Namespace, numNamespaces)

	for i := 0; i < numNamespaces; i++ {
		ns := corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("stress-test-ns-%d", i),
				Labels: map[string]string{
					"nvca.nvcf.nvidia.io/workload-instance-type": "miniservice",
				},
			},
		}
		orphanedNamespaces[i] = ns
	}

	// Create k8s objects
	k8sObjects := make([]runtime.Object, numNamespaces)
	for i := range orphanedNamespaces {
		k8sObjects[i] = &orphanedNamespaces[i]
	}
	k8sClient := fake.NewSimpleClientset(k8sObjects...)
	nvcaClient := bartfake.NewSimpleClientset()

	// Simulate some random failures to test error handling under concurrency
	var deletionAttempts sync.Map
	k8sClient.PrependReactor("delete", "namespaces", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		deleteAction := action.(k8stesting.DeleteAction)
		nsName := deleteAction.GetName()

		// Store each deletion attempt
		if _, loaded := deletionAttempts.LoadOrStore(nsName, true); loaded {
			// Namespace was already attempted to be deleted
			t.Errorf("Namespace %s was attempted to be deleted multiple times - potential data race", nsName)
		}

		// Fail 10% of deletions randomly to test error handling
		hash := 0
		for _, c := range nsName {
			hash += int(c)
		}
		if hash%10 == 0 {
			return true, nil, errors.New("simulated deletion failure")
		}

		return false, nil, nil
	})

	cleaner := NewCleaner(k8sClient, nvcaClient, &mockICMSRequestGetter{}, nil)
	err := cleaner.executeCleanup(ctx, orphanedNamespaces)

	// Should always complete without error even if some deletions fail
	assert.NoError(t, err)

	// Verify that we attempted to delete each namespace exactly once
	uniqueDeletions := 0
	deletionAttempts.Range(func(key, value interface{}) bool {
		uniqueDeletions++
		return true
	})

	// We should have attempted to delete all namespaces
	assert.Equal(t, numNamespaces, uniqueDeletions, "Each namespace should be deleted exactly once")
}

// Test Run method with large number of namespaces
func TestCleaner_Run_LargeScale(t *testing.T) {
	ctx := context.Background()

	// Create many namespaces
	const numNamespaces = 50
	namespaces := make([]runtime.Object, numNamespaces)
	for i := 0; i < numNamespaces; i++ {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("orphaned-ns-%d", i),
				Labels: map[string]string{
					"nvca.nvcf.nvidia.io/workload-instance-type": "miniservice",
				},
			},
		}
		namespaces[i] = ns
	}

	// Create fake k8s client
	k8sClient := fake.NewSimpleClientset(namespaces...)
	nvcaClient := bartfake.NewSimpleClientset()

	// Create mock ICMSRequest getter (no requests exist)
	mockICMSGetter := &mockICMSRequestGetter{
		requests: make(map[string]*nvcav2beta1.ICMSRequest),
	}

	// Create cleaner and test
	cleaner := NewCleaner(k8sClient, nvcaClient, mockICMSGetter, nil)
	err := cleaner.Run(ctx)

	assert.NoError(t, err)

	// Verify all namespaces were cleaned up
	remainingNs, err := k8sClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, len(remainingNs.Items))
}

// Test parallel cleanup of namespaces with complex resources
func TestCleaner_Run_ParallelComplexResources(t *testing.T) {
	ctx := context.Background()

	const numNamespaces = 100
	k8sObjects := make([]runtime.Object, 0)
	nvcaObjects := make([]runtime.Object, 0)

	// Create a mix of namespaces with different resource patterns
	for i := 0; i < numNamespaces; i++ {
		nsName := fmt.Sprintf("complex-ns-%d", i)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: nsName,
				Labels: map[string]string{
					"nvca.nvcf.nvidia.io/workload-instance-type": func() string {
						return "miniservice"
					}(),
				},
			},
		}
		k8sObjects = append(k8sObjects, ns)

		// Add StorageRequests to some namespaces
		if i%3 == 0 {
			st := &nvcav2beta1.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("storage-req-%d", i),
					Namespace: nsName,
					Finalizers: func() []string {
						if i%5 == 0 {
							return []string{"test-finalizer"}
						}
						return nil
					}(),
				},
				Spec: nvcav2beta1.StorageRequestSpec{
					Type:             nvcav2beta1.SharedStorageRequest,
					RequestName:      fmt.Sprintf("storage-req-%d", i),
					RequestNamespace: types.DefaultICMSRequestNamespace,
				},
			}
			nvcaObjects = append(nvcaObjects, st)
		}

		// Add PVCs to some namespaces
		if i%4 == 0 {
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("pvc-%d", i),
					Namespace: nsName,
					Finalizers: func() []string {
						if i%6 == 0 {
							return []string{"kubernetes.io/pvc-protection"}
						}
						return nil
					}(),
				},
			}
			k8sObjects = append(k8sObjects, pvc)
		}
	}

	k8sClient := fake.NewSimpleClientset(k8sObjects...)
	nvcaClient := bartfake.NewSimpleClientset(nvcaObjects...)

	// No ICMSRequests exist
	mockICMSGetter := &mockICMSRequestGetter{
		requests: make(map[string]*nvcav2beta1.ICMSRequest),
	}

	cleaner := NewCleaner(k8sClient, nvcaClient, mockICMSGetter, nil)
	err := cleaner.Run(ctx)

	assert.NoError(t, err)

	// Verify all namespaces were cleaned up
	remainingNs, err := k8sClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, len(remainingNs.Items), "All namespaces should be cleaned up")
}
