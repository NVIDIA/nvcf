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

package storage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
)

func TestCleanupDanglingStorageRequest(t *testing.T) {
	// Setup shared storage request, and perform a standard cleanup on happy path
	tests := []struct {
		name                      string
		owner                     *nvcav1new.StorageRequest
		expectedPatchFuncCalled   bool
		expectedCleanupFuncCalled bool
		expectedFinalizers        []string
		expectedResult            reconcile.Result
		patchFuncErr              error
		cleanupFuncErr            error
		cleanupConditionStatus    metav1.ConditionStatus
	}{
		{
			name: "happy path storage request already cleaned up",
			owner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:  "sr-12345",
					Name:       nvcav1new.SharedStorageRequest.Name(),
					Finalizers: []string{},
				},
			},
			expectedPatchFuncCalled:   true, // Function always calls patch
			expectedCleanupFuncCalled: true,
			patchFuncErr:              nil,
			cleanupFuncErr:            nil,
			cleanupConditionStatus:    metav1.ConditionFalse, // Cleanup will set this initially
			expectedResult:            reconcile.Result{},    // No requeue if patch succeeds
		},
		{
			name: "happy path storage request needs cleaned up",
			owner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:  "sr-12345",
					Name:       nvcav1new.SharedStorageRequest.Name(),
					Finalizers: []string{"foo", StorageRequestFinalizer},
				},
			},
			expectedPatchFuncCalled:   true,
			expectedCleanupFuncCalled: true,
			expectedFinalizers:        []string{"foo"},
			patchFuncErr:              nil,
			cleanupFuncErr:            nil,
			cleanupConditionStatus:    metav1.ConditionTrue,
			expectedResult:            reconcile.Result{},
		},
		{
			name: "cleanup successful but patch fails",
			owner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:  "sr-12345",
					Name:       nvcav1new.SharedStorageRequest.Name(),
					Finalizers: []string{"foo", StorageRequestFinalizer},
				},
			},
			expectedPatchFuncCalled:   true,
			expectedCleanupFuncCalled: true,
			expectedFinalizers:        []string{"foo"},
			patchFuncErr:              errors.New("patch error"),
			cleanupFuncErr:            nil,
			cleanupConditionStatus:    metav1.ConditionTrue,
			expectedResult:            reconcile.Result{},
		},
		{
			name: "patch fails with conflict error - should requeue",
			owner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:  "sr-12345",
					Name:       nvcav1new.SharedStorageRequest.Name(),
					Finalizers: []string{"foo", StorageRequestFinalizer},
				},
			},
			expectedPatchFuncCalled:   true,
			expectedCleanupFuncCalled: true,
			expectedFinalizers:        []string{"foo"},
			patchFuncErr:              apierrors.NewConflict(corev1.Resource("storagerequest"), "test", errors.New("conflict")),
			cleanupFuncErr:            nil,
			cleanupConditionStatus:    metav1.ConditionTrue,
			expectedResult:            reconcile.Result{RequeueAfter: defaultRequeueDelay},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			patchFuncCalled := false
			cleanupFuncCalled := false
			ctx := context.Background()

			doCleanupFunc := func(ctx context.Context, st *nvcav1new.StorageRequest) (reconcile.Result, error) {
				cleanupFuncCalled = true
				// Set the cleanup condition based on test expectation
				meta.SetStatusCondition(&st.Status.Conditions, metav1.Condition{
					Type:   ConditionTypeCleanupSuccessful,
					Status: test.cleanupConditionStatus,
					Reason: ConditionReasonAllObjectsDeleted,
				})
				return reconcile.Result{}, test.cleanupFuncErr
			}

			patchFunc := func(ctx context.Context, st *nvcav1new.StorageRequest, stCopy *nvcav1new.StorageRequest) error {
				patchFuncCalled = true
				assert.ElementsMatch(t, test.expectedFinalizers, stCopy.GetFinalizers())
				return test.patchFuncErr
			}

			res, err := cleanupDanglingStorageRequest(ctx, test.owner, doCleanupFunc, patchFunc)
			assert.Equal(t, test.expectedPatchFuncCalled, patchFuncCalled)
			assert.Equal(t, test.expectedCleanupFuncCalled, cleanupFuncCalled)
			assert.Equal(t, test.expectedResult, res)
			if test.patchFuncErr != nil && !apierrors.IsConflict(test.patchFuncErr) {
				// For non-conflict patch errors, function should return error
				if test.cleanupFuncErr != nil {
					assert.NotNil(t, err)
					assert.Contains(t, err.Error(), test.patchFuncErr.Error())
				} else {
					assert.Equal(t, test.patchFuncErr, err) // Should be the patch error
				}
			} else if apierrors.IsConflict(test.patchFuncErr) {
				// For conflict errors, the function should return the cleanup error (nil in our tests)
				assert.Equal(t, test.cleanupFuncErr, err)
			} else {
				assert.Equal(t, test.cleanupFuncErr, err)
			}
		})
	}
}

func TestRequeueDeletingStorageRequestWithFinalizer(t *testing.T) {
	deletionTime := metav1.Now()
	existingResult := reconcile.Result{RequeueAfter: defaultRequeueDelay}

	tests := []struct {
		name     string
		res      reconcile.Result
		err      error
		owner    *nvcav1new.StorageRequest
		expected reconcile.Result
	}{
		{
			name: "deleting storage request with finalizer requeues",
			owner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &deletionTime,
					Finalizers:        []string{StorageRequestFinalizer},
				},
			},
			expected: reconcile.Result{RequeueAfter: deletingStorageRequestRequeueDelay},
		},
		{
			name: "deleting storage request without finalizer does not requeue",
			owner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &deletionTime,
				},
			},
			expected: reconcile.Result{},
		},
		{
			name: "active storage request with finalizer does not requeue",
			owner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Finalizers: []string{StorageRequestFinalizer},
				},
			},
			expected: reconcile.Result{},
		},
		{
			name: "existing result is preserved",
			res:  existingResult,
			owner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &deletionTime,
					Finalizers:        []string{StorageRequestFinalizer},
				},
			},
			expected: existingResult,
		},
		{
			name: "error result is preserved",
			res:  existingResult,
			err:  errors.New("cleanup failed"),
			owner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &deletionTime,
					Finalizers:        []string{StorageRequestFinalizer},
				},
			},
			expected: existingResult,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := requeueDeletingStorageRequestWithFinalizer(test.res, test.err, test.owner)
			assert.Equal(t, test.expected, got)
		})
	}
}

func TestDoCleanupSharedStorage(t *testing.T) {
	// Setup shared storage request, and perform a standard cleanup on happy path
	tests := []struct {
		name                    string
		listFunc                func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
		deleteAllOfFunc         func(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error
		deleteFunc              func(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error
		patchFunc               func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error
		owner                   *nvcav1new.StorageRequest
		expectedPatchFuncCalled bool
		expectedResult          reconcile.Result
		expectedErr             error
		expectedOwner           *nvcav1new.StorageRequest
	}{
		{
			name: "happy path storage request already cleaned up",
			owner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-12345",
					Name:      nvcav1new.SharedStorageRequest.Name(),
				},
				Status: nvcav1new.StorageRequestStatus{
					Conditions: []metav1.Condition{
						{
							Type:   ConditionTypeCleanupSuccessful,
							Status: metav1.ConditionTrue,
							Reason: ConditionReasonAllObjectsDeleted,
						},
					},
				},
			},
			expectedOwner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-12345",
					Name:      nvcav1new.SharedStorageRequest.Name(),
				},
				Status: nvcav1new.StorageRequestStatus{
					Conditions: []metav1.Condition{
						{
							Type:   ConditionTypeCleanupSuccessful,
							Status: metav1.ConditionTrue,
							Reason: ConditionReasonAllObjectsDeleted,
						},
					},
				},
			},
			expectedPatchFuncCalled: false,
		},
		{
			name: "happy path storage request with all types present, and PVCs ensure a requeue",
			listFunc: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
				listType := reflect.TypeOf(list)
				if reflect.TypeOf(&storagev1.StorageClassList{}) == listType {
					scList := list.(*storagev1.StorageClassList)
					scList.Items = append(scList.Items, storagev1.StorageClass{
						ObjectMeta: metav1.ObjectMeta{
							Name: "sr-12345",
							Finalizers: []string{
								StorageRequestFinalizer,
							},
						},
					})
				} else if reflect.TypeOf(&batchv1.JobList{}) == listType {
					itemList := list.(*batchv1.JobList)
					itemList.Items = append(itemList.Items, batchv1.Job{
						ObjectMeta: metav1.ObjectMeta{
							Name: "sr-12345",
							OwnerReferences: []metav1.OwnerReference{
								{
									UID: types.UID("12345-abcdef"),
								},
							},
						},
					})
				} else if reflect.TypeOf(&corev1.PodList{}) == listType {
					itemList := list.(*corev1.PodList)
					itemList.Items = append(itemList.Items, corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name: "sr-12345",
							OwnerReferences: []metav1.OwnerReference{
								{
									UID: types.UID("12345-abcdef"),
								},
							},
						},
					})
				} else if reflect.TypeOf(&corev1.ResourceQuotaList{}) == listType {
					itemList := list.(*corev1.ResourceQuotaList)
					itemList.Items = append(itemList.Items, corev1.ResourceQuota{
						ObjectMeta: metav1.ObjectMeta{
							Name: "sr-12345",
							OwnerReferences: []metav1.OwnerReference{
								{
									UID: types.UID("12345-abcdef"),
								},
							},
						},
					})
				} else if reflect.TypeOf(&corev1.PersistentVolumeClaimList{}) == listType {
					itemList := list.(*corev1.PersistentVolumeClaimList)
					itemList.Items = append(itemList.Items, corev1.PersistentVolumeClaim{
						ObjectMeta: metav1.ObjectMeta{
							Name: "sr-12345-pvc",
							OwnerReferences: []metav1.OwnerReference{
								{
									UID: types.UID("12345-abcdef"),
								},
							},
						},
					})
				}

				return nil
			},
			patchFunc: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				require.IsType(t, &storagev1.StorageClass{}, obj, "patch should only be on storageclass")
				assert.Empty(t, obj.GetFinalizers())
				return nil
			},
			owner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-12345",
					Name:      nvcav1new.SharedStorageRequest.Name(),
					UID:       types.UID("12345-abcdef"),
				},
			},
			expectedOwner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-12345",
					Name:      nvcav1new.SharedStorageRequest.Name(),
					UID:       types.UID("12345-abcdef"),
				},
				Status: nvcav1new.StorageRequestStatus{
					Conditions: []metav1.Condition{
						{
							Type:    ConditionTypeCleanupSuccessful,
							Status:  metav1.ConditionFalse,
							Reason:  ConditionReasonSomeObjectsPendingDeletion,
							Message: fmt.Sprintf("PVCs: %q", []string{"sr-12345-pvc"}),
						},
					},
				},
			},
			expectedPatchFuncCalled: false,
			expectedResult:          reconcile.Result{Requeue: true},
		},
		{
			name: "happy path storage request with all types present, and PVs ensure a requeue",
			listFunc: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
				listType := reflect.TypeOf(list)
				if reflect.TypeOf(&storagev1.StorageClassList{}) == listType {
					scList := list.(*storagev1.StorageClassList)
					scList.Items = append(scList.Items, storagev1.StorageClass{
						ObjectMeta: metav1.ObjectMeta{
							Name: "sr-12345",
							Finalizers: []string{
								StorageRequestFinalizer,
							},
						},
					})
				} else if reflect.TypeOf(&batchv1.JobList{}) == listType {
					itemList := list.(*batchv1.JobList)
					itemList.Items = append(itemList.Items, batchv1.Job{
						ObjectMeta: metav1.ObjectMeta{
							Name: "sr-12345",
							OwnerReferences: []metav1.OwnerReference{
								{
									UID: types.UID("12345-abcdef"),
								},
							},
						},
					})
				} else if reflect.TypeOf(&corev1.PodList{}) == listType {
					itemList := list.(*corev1.PodList)
					itemList.Items = append(itemList.Items, corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name: "sr-12345",
							OwnerReferences: []metav1.OwnerReference{
								{
									UID: types.UID("12345-abcdef"),
								},
							},
						},
					})
				} else if reflect.TypeOf(&corev1.ResourceQuotaList{}) == listType {
					itemList := list.(*corev1.ResourceQuotaList)
					itemList.Items = append(itemList.Items, corev1.ResourceQuota{
						ObjectMeta: metav1.ObjectMeta{
							Name: "sr-12345",
							OwnerReferences: []metav1.OwnerReference{
								{
									UID: types.UID("12345-abcdef"),
								},
							},
						},
					})
				} else if reflect.TypeOf(&corev1.PersistentVolumeList{}) == listType {
					itemList := list.(*corev1.PersistentVolumeList)
					itemList.Items = append(itemList.Items, corev1.PersistentVolume{
						ObjectMeta: metav1.ObjectMeta{
							Name: "sr-12345-pv",
							OwnerReferences: []metav1.OwnerReference{
								{
									UID: types.UID("12345-abcdef"),
								},
							},
						},
					})
				}

				return nil
			},
			patchFunc: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				require.IsType(t, &storagev1.StorageClass{}, obj, "patch should only be on storageclass")
				assert.Empty(t, obj.GetFinalizers())
				return nil
			},
			owner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-12345",
					Name:      nvcav1new.SharedStorageRequest.Name(),
					UID:       types.UID("12345-abcdef"),
				},
			},
			expectedOwner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-12345",
					Name:      nvcav1new.SharedStorageRequest.Name(),
					UID:       types.UID("12345-abcdef"),
				},
				Status: nvcav1new.StorageRequestStatus{
					Conditions: []metav1.Condition{
						{
							Type:    ConditionTypeCleanupSuccessful,
							Status:  metav1.ConditionFalse,
							Reason:  ConditionReasonSomeObjectsPendingDeletion,
							Message: fmt.Sprintf("PVs: %q", []string{"sr-12345-pv"}),
						},
					},
				},
			},
			expectedPatchFuncCalled: false,
			expectedResult:          reconcile.Result{Requeue: true},
		},
		{
			name: "happy path storage request with everything already gone, except the storage class",
			listFunc: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
				if reflect.TypeOf(&storagev1.StorageClassList{}) == reflect.TypeOf(list) {
					scList := list.(*storagev1.StorageClassList)
					scList.Items = append(scList.Items, storagev1.StorageClass{
						ObjectMeta: metav1.ObjectMeta{
							Name: "sr-12345",
							Finalizers: []string{
								StorageRequestFinalizer,
							},
						},
					})
				}

				return nil
			},
			patchFunc: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				require.IsType(t, &storagev1.StorageClass{}, obj, "patch should only be on storageclass")
				assert.Empty(t, obj.GetFinalizers())
				return nil
			},
			owner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-12345",
					Name:      nvcav1new.SharedStorageRequest.Name(),
					UID:       types.UID("12345-abcdef"),
				},
			},
			expectedOwner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-12345",
					Name:      nvcav1new.SharedStorageRequest.Name(),
					UID:       types.UID("12345-abcdef"),
				},
				Status: nvcav1new.StorageRequestStatus{
					Conditions: []metav1.Condition{
						{
							Type:   ConditionTypeCleanupSuccessful,
							Status: metav1.ConditionTrue,
							Reason: ConditionReasonAllObjectsDeleted,
						},
					},
				},
			},
			expectedPatchFuncCalled: true,
		},
		{
			name: "storage request finalizer removal failed",
			listFunc: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
				if reflect.TypeOf(&storagev1.StorageClassList{}) == reflect.TypeOf(list) {
					scList := list.(*storagev1.StorageClassList)
					scList.Items = append(scList.Items, storagev1.StorageClass{
						ObjectMeta: metav1.ObjectMeta{
							Name: "sr-12345",
							Finalizers: []string{
								StorageRequestFinalizer,
							},
						},
					})
				}

				return nil
			},
			patchFunc: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				require.IsType(t, &storagev1.StorageClass{}, obj, "patch should only be on storageclass")
				assert.Empty(t, obj.GetFinalizers())
				return errors.New("storage class finalizer removal failed")
			},
			owner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-12345",
					Name:      nvcav1new.SharedStorageRequest.Name(),
					UID:       types.UID("12345-abcdef"),
				},
			},
			expectedOwner: &nvcav1new.StorageRequest{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "sr-12345",
					Name:      nvcav1new.SharedStorageRequest.Name(),
					UID:       types.UID("12345-abcdef"),
				},
				Status: nvcav1new.StorageRequestStatus{
					Conditions: []metav1.Condition{
						{
							Type:    ConditionTypeCleanupSuccessful,
							Status:  metav1.ConditionFalse,
							Reason:  ConditionReasonSomeObjectsPendingDeletion,
							Message: "storage class errors: [\"remove finalizer from storage class sr-12345 status: storage class finalizer removal failed\"]",
						},
					},
				},
			},
			expectedPatchFuncCalled: true,
			expectedResult:          reconcile.Result{},
			expectedErr:             errors.Join(fmt.Errorf("remove finalizer from storage class %s status: %w", "sr-12345", errors.New("storage class finalizer removal failed"))),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			patchFuncCalled := false
			ctx := context.Background()
			k8sClient := &mockCleanK8sClient{
				listFunc: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
					if test.listFunc != nil {
						return test.listFunc(ctx, list, opts...)
					}
					return nil
				},
				deleteAllOfFunc: func(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
					if test.deleteAllOfFunc != nil {
						return test.deleteAllOfFunc(ctx, obj, opts...)
					}
					return nil
				},
				deleteFunc: func(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
					if test.deleteFunc != nil {
						return test.deleteFunc(ctx, obj, opts...)
					}
					return nil
				},
				patchFunc: func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					patchFuncCalled = true
					if test.patchFunc != nil {
						return test.patchFunc(ctx, obj, patch, opts...)
					}
					return nil
				},
			}
			res, err := doCleanupNamespaced(ctx, k8sClient, test.owner)
			assert.Equal(t, test.expectedPatchFuncCalled, patchFuncCalled)
			assert.Equal(t, test.expectedErr, err)
			assert.Equal(t, test.expectedResult, res)

			// Zero out timestamps to not match on those
			for i := range test.owner.Status.Conditions {
				test.owner.Status.Conditions[i].LastTransitionTime = metav1.Time{}
			}
			assert.Equal(t, test.expectedOwner, test.owner)
		})
	}
}

func Test_findAndDecodeCacheArtifacts(t *testing.T) {
	r := &Reconciler{
		fff:     &featureflagmock.Fetcher{},
		metrics: newTestMetrics(),
	}

	type spec struct {
		name        string
		icmsReqSpec nvcav2beta1.ICMSRequestSpec
		expError    error
	}

	cases := []spec{
		// TODO: container function/task when supported by storage.
		{
			name: "helm function",
			icmsReqSpec: nvcav2beta1.ICMSRequestSpec{
				FunctionDetails: function.Details{
					FunctionID:        "funcid-1",
					FunctionVersionID: "funcverid-1",
					FunctionType:      "DEFAULT",
				},
				Action:         common.FunctionCreationAction,
				NCAId:          "ncaid-1",
				RequestID:      "reqid1",
				MessageBatchID: "mbatchid1",
				CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
					CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
						Action:            common.FunctionCreationAction,
						RequestID:         "reqid1",
						MessageBatchID:    "mbatchid1",
						InstanceType:      "ON-PREM.GPU.L40",
						InstanceTypeName:  "ON-PREM.GPU.L40_1x",
						InstanceTypeValue: "ON-PREM.GPU.L40",
						GPUType:           "L40",
						RequestedGPUCount: 1,
						InstanceCount:     1,
						NCAID:             "ncaid-1",
					},
					FunctionLaunchSpecification: &function.LaunchSpecification{
						CloudProvider:   "DGXCLOUD",
						ICMSEnvironment: "prod",
						GPUName:         "L40",
						EnvironmentB64:  "QVRUQUNIRURfR1BVX0NPVU5UPSIxIgpCWU9PX09URUxfQ09MTEVDVE9SX0NPTlRBSU5FUj1udmNyLmlvL3F0ZnB0MWgwYmlldS9udmNmLWNvcmUvYnlvby1vdGVsLWNvbGxlY3RvcjoxLjIuMwpDTE9VRF9QUk9WSURFUj1PTi1QUkVNCkVTU19BR0VOVF9DT05UQUlORVI9bnZjci5pby9udi1jZi9udmNmLWNvcmUvZXNzLWFnZW50OjEuMC4wCkZVTkNUSU9OX0lEPTVhM2Q0YTdlLTllZTMtNDc2Mi04ZDM3LWQzYjQwYTZmODRjNgpGVU5DVElPTl9OQU1FPW15LWZ1bmMKRlVOQ1RJT05fVkVSU0lPTl9JRD0yYzk0OGQ5Yi1kYjVkLTRmOTMtOGMyOS1mNWQ4YTVkODljYjkKR1BVX05BTUU9TDQwCkhFTE1fQ0hBUlRfSU5GRVJFTkNFX1NFUlZJQ0VfTkFNRT1teXNlcnZpY2UKQ09OVEFJTkVSX1JFR0lTVFJJRVNfQ1JFREVOVElBTFM9ZXlKck9ITlRaV055WlhSeklqcGJleUpoZFhSb2N5STZleUp1ZG1OeUxtbHZJanA3SW1GMWRHZ2lPaUpqTTFKdVRGZEdhVmw2UlhsTmVtOHlXbXBaZWs1SFVUUk9VekExVFhwR2JFeFVVWGhhYWxsMFdWUktiRmw1TURKT01razBUbnBWZDFwcVJYbE9NbFU5SW4xOWZWMTkKSU5GRVJFTkNFX0NPTlRBSU5FUl9FTlY9VzNzaWEyVjVJam9pU1U1R1JWSkZUa05GWDBWT1ZsOUxSVmtpTENKMllXeDFaU0k2SW1sdVptVnlaVzVqWlY5MllXeDFaU0o5WFE9PQpJTkZFUkVOQ0VfSEVBTFRIX0VORFBPSU5UPS92Mi9oZWFsdGgvcmVhZHkKSU5GRVJFTkNFX0hFQUxUSF9FWFBFQ1RFRF9SRVNQT05TRV9DT0RFPSIyMDAiCklORkVSRU5DRV9IRUFMVEhfUE9SVD0iNTAwNTEiCklORkVSRU5DRV9QT1JUPSI1MDA1MSIKSU5GRVJFTkNFX1BST1RPQ09MPUdSUEMKSU5GRVJFTkNFX1VSTD0vZ3JwYwpJTklUX0NPTlRBSU5FUj1udmNyLmlvL3F0ZnB0MWgwYmlldS9udmNmLWNvcmUvbnZjZl93b3JrZXJfaW5pdDowLjI0LjEwCk1BWF9SRVFVRVNUX0NPTkNVUlJFTkNZPSIxIgpOQ0FfSUQ9X2xJTFhCLTFOZk5tQm5RU2tfc3BxVldPdENBWFFtNTBVRU13ajNUUmd5bUpKMkF5dXdjZ3hxCk5WQ0ZfRlFETj1odHRwczovL3VzLXdlc3QtMi5hcGkubnZjZi5udmlkaWEuY29tCk5WQ0ZfRlFETl9HUlBDPWh0dHBzOi8vZ3JwYy5hcGkubnZjZi5udmlkaWEuY29tCk5WQ0ZfRlFETl9OQVRTPXRsczovL3VzLXdlc3QtMi5hd3MuY2xvdWQubmF0cy5udmNmLm52aWRpYS5jb206NDIyMgpOVkNGX1dPUktFUl9UT0tFTj10b2sKT1RFTF9DT05UQUlORVI9bnZjci5pby9xdGZwdDFoMGJpZXUvbnZjZi1jb3JlL29wZW50ZWxlbWV0cnktY29sbGVjdG9yOjAuNzQuMApPVEVMX0VYUE9SVEVSX09UTFBfRU5EUE9JTlQ9aHR0cHM6Ly9wcm9kLm90ZWwua2FpemVuLm52aWRpYS5jb206ODI4MgpTRUNSRVRTX0FTU0VSVElPTl9UT0tFTj1leUpoYkdjaU9pSlNVekkxTmlJc0luUjVjQ0k2SWtwWFZDSjkuZXlKemRXSWlPaUl4TWpNME5UWTNPRGt3SWl3aWJtRnRaU0k2SWtwdmFHNGdSRzlsSWl3aVlYTnpaWEowYVc5dUlqcDdJbk5sWTNKbGRGQmhkR2h6SWpwYkltRmpZMjkxYm5SekwxOXNTVXhZUWkweFRtWk9iVUp1VVZOclgzTndjVlpYVDNSRFFWaFJiVFV3VlVWTmQyb3pWRkpuZVcxS1NqSkJlWFYzWTJkNGNTOTBaV3hsYldWMGNua3ZObVpoT0RNMk5tVXROMkpoTWkwME1UUXpMV0UzTkRRdE56VXhOV1poWmpsaVpqY3lJaXdpWVdOamIzVnVkSE12WDJ4SlRGaENMVEZPWms1dFFtNVJVMnRmYzNCeFZsZFBkRU5CV0ZGdE5UQlZSVTEzYWpOVVVtZDViVXBLTWtGNWRYZGpaM2h4TDNSbGJHVnRaWFJ5ZVM4MlptRTRNelkyWlMwM1ltRXlMVFF4TkRNdFlUYzBOQzAzTlRFMVptRm1PV0ptT0RFaVhYMHNJbUZrYldsdUlqcDBjblZsTENKcFlYUWlPakUxTVRZeU16a3dNako5LlNwUVliRmUxbmZyUTVLc2hSbHk5U1VDMjZXX2oycFFoNkRNaW5zYnJzUUh2S2cxc2Uyb0gzVnpvaW5iTWJRel81TFhjZy1YTmt4NGNOSk4yQWp1d1VJems2RElVTElDSGVxdWpxLXhBYWdGUjhfejI1bzExZDAxekJTNU54RjlBQ2d0SWw2OWRoVEhrOHNLMmVRYjRBRkdDRmZmNjFqMGtYYWJJWUVTR0p4ZHY5UmtOZld0WVotRm1JYzl1RjRqWTU5elIxRUJkWGlsY2NjUjBSaUN2S0FsVFlvckU3VGotMDRLZ1RGbnZRYm1QMFRRR1FkNnhicWRBYVBSQnBYeUJHMDRxbUEyOTZUZnJBT1ZfMDJhSWR0akhhNVNqbXZ0UEFiVmVIVlY1QnhfWmQ4eVZteU4wZTdxZWduQU9xYzVOUDNrRjM4VzRuV2hURThWa05UWmpUUEEKU0lERUNBUl9SRUdJU1RSWV9DUkVERU5USUFMPWV5SmhkWFJvY3lJNmV5SnVkbU55TG1sdklqcDdJbUYxZEdnaU9pSktSemxvWkZoU2IyUkhPWEphVnpRMlltNWFhR05IYTNSak0xSnVURmRHYVZsNlJYbE5kejA5SW4xOWZRbz0KVFJBQ0lOR19BQ0NFU1NfVE9LRU49dHJhY2UtdG9rLTEKVVRJTFNfQ09OVEFJTkVSPW52Y3IuaW8vcXRmcHQxaDBiaWV1L252Y2YtY29yZS9udmNmX3dvcmtlcl91dGlsczoyLjIxLjQKSEVMTV9SRUdJU1RSSUVTX0NSRURFTlRJQUxTPWV5SnJPSE5UWldOeVpYUnpJanBiZXlKaGRYUm9jeUk2ZXlKb1pXeHRMbTVuWXk1dWRtbGthV0V1WTI5dElqcDdJbUYxZEdnaU9pSmpNMUp1VEZkR2FWbDZSWGxOZW04eVdtcFplazVIVVRST1V6QTFUWHBHYkV4VVVYaGFhbGwwV1ZSS2JGbDVNREpPTWtrMFRucFZkMXBxUlhsT01sVTlJbjE5ZlYxOQo=",
						CacheLaunchSpecification: &common.CacheLaunchSpecification{
							CacheArtifacts: true,
							CacheHandle:    "abc123handle",
							CacheSize:      262144000,
						},
						HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
							HelmChartURL: "https://foo/bar.tgz",
						},
					},
				},
			},
		},
		{
			name: "helm task",
			icmsReqSpec: nvcav2beta1.ICMSRequestSpec{
				TaskDetails: task.Details{
					TaskID:   "funcid-1",
					TaskType: "HELMCHART",
				},
				Action:         common.TaskCreationAction,
				NCAId:          "ncaid-1",
				RequestID:      "reqid1",
				MessageBatchID: "mbatchid1",
				CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
					CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
						Action:            common.TaskCreationAction,
						RequestID:         "reqid1",
						MessageBatchID:    "mbatchid1",
						InstanceType:      "ON-PREM.GPU.L40",
						InstanceTypeName:  "ON-PREM.GPU.L40_1x",
						InstanceTypeValue: "ON-PREM.GPU.L40",
						GPUType:           "L40",
						RequestedGPUCount: 1,
						InstanceCount:     1,
						NCAID:             "ncaid-1",
						AccountName:       "account1",
					},
					TaskLaunchSpecification: &task.LaunchSpecification{
						CloudProvider:                  "DGXCLOUD",
						ICMSEnvironment:                "prod",
						ResultHandlingStrategy:         "UPLOAD",
						TerminationGracePeriodDuration: "PT10M",
						MaxRuntimeDuration:             "PT2H",
						MaxQueuedDuration:              "PT2M",
						EnvironmentB64:                 "RVNTX0FHRU5UX0NPTlRBSU5FUj1zdGcubnZjci5pby9udi1jZi9udmNmLWNvcmUvZXNzLWFnZW50OjEuMC4wCklOSVRfQ09OVEFJTkVSPW52Y3IuaW8vcXRmcHQxaDBiaWV1L252Y2YtY29yZS9udmNmX3dvcmtlcl9pbml0OjAuMjQuMTAKTUFYX1JFUVVFU1RfQ09OQ1VSUkVOQ1k9IjEiCk5DQV9JRD1fbElMWEItMU5mTm1CblFTa19zcHFWV090Q0FYUW01MFVFTXdqM1RSZ3ltSkoyQXl1d2NneHEKTlZDRl9GUUROPWh0dHBzOi8vdXMtd2VzdC0yLmFwaS5udmNmLm52aWRpYS5jb20KTlZDRl9GUUROX0dSUEM9aHR0cHM6Ly9ncnBjLmFwaS5udmNmLm52aWRpYS5jb20KTlZDRl9GUUROX05BVFM9dGxzOi8vdXMtd2VzdC0yLmF3cy5jbG91ZC5uYXRzLm52Y2YubnZpZGlhLmNvbTo0MjIyCk5WQ0ZfV09SS0VSX1RPS0VOPXRvawpPVEVMX0NPTlRBSU5FUj1udmNyLmlvL3F0ZnB0MWgwYmlldS9udmNmLWNvcmUvb3BlbnRlbGVtZXRyeS1jb2xsZWN0b3I6MC43NC4wCk9URUxfRVhQT1JURVJfT1RMUF9FTkRQT0lOVD1odHRwczovL3Byb2Qub3RlbC5rYWl6ZW4ubnZpZGlhLmNvbTo4MjgyClNFQ1JFVFNfQVNTRVJUSU9OX1RPS0VOPWV5SmhiR2NpT2lKU1V6STFOaUlzSW5SNWNDSTZJa3BYVkNKOS5leUp6ZFdJaU9pSXhNak0wTlRZM09Ea3dJaXdpYm1GdFpTSTZJa3B2YUc0Z1JHOWxJaXdpWVhOelpYSjBhVzl1SWpwN0luTmxZM0psZEZCaGRHaHpJanBiSW1GalkyOTFiblJ6TDE5c1NVeFlRaTB4VG1aT2JVSnVVVk5yWDNOd2NWWlhUM1JEUVZoUmJUVXdWVVZOZDJvelZGSm5lVzFLU2pKQmVYVjNZMmQ0Y1M5MFpXeGxiV1YwY25rdk5tWmhPRE0yTm1VdE4ySmhNaTAwTVRRekxXRTNORFF0TnpVeE5XWmhaamxpWmpjeUlpd2lZV05qYjNWdWRITXZYMnhKVEZoQ0xURk9aazV0UW01UlUydGZjM0J4VmxkUGRFTkJXRkZ0TlRCVlJVMTNhak5VVW1kNWJVcEtNa0Y1ZFhkalozaHhMM1JsYkdWdFpYUnllUzgyWm1FNE16WTJaUzAzWW1FeUxUUXhORE10WVRjME5DMDNOVEUxWm1GbU9XSm1PREVpWFgwc0ltRmtiV2x1SWpwMGNuVmxMQ0pwWVhRaU9qRTFNVFl5TXprd01qSjkuU3BRWWJGZTFuZnJRNUtzaFJseTlTVUMyNldfajJwUWg2RE1pbnNicnNRSHZLZzFzZTJvSDNWem9pbmJNYlF6XzVMWGNnLVhOa3g0Y05KTjJBanV3VUl6azZESVVMSUNIZXF1anEteEFhZ0ZSOF96MjVvMTFkMDF6QlM1TnhGOUFDZ3RJbDY5ZGhUSGs4c0syZVFiNEFGR0NGZmY2MWowa1hhYklZRVNHSnhkdjlSa05mV3RZWi1GbUljOXVGNGpZNTl6UjFFQmRYaWxjY2NSMFJpQ3ZLQWxUWW9yRTdUai0wNEtnVEZudlFibVAwVFFHUWQ2eGJxZEFhUFJCcFh5QkcwNHFtQTI5NlRmckFPVl8wMmFJZHRqSGE1U2ptdnRQQWJWZUhWVjVCeF9aZDh5Vm15TjBlN3FlZ25BT3FjNU5QM2tGMzhXNG5XaFRFOFZrTlRaalRQQQpTSURFQ0FSX1JFR0lTVFJZX0NSRURFTlRJQUw9ZXlKaGRYUm9jeUk2ZXlKdWRtTnlMbWx2SWpwN0ltRjFkR2dpT2lKS1J6bG9aRmhTYjJSSE9YSmFWelEyWW01YWFHTkhhM1JqTTFKdVRGZEdhVmw2UlhsTmR6MDlJbjE5ZlFvPQpUQVNLX0NPTlRBSU5FUj1udmNyLmlvL215b3JnL2dwdC0zLjUtdHVyYm8tZmluZS10dW5lOjEuMC4wClRBU0tfQ09OVEFJTkVSX0FSR1M9LWFyZzE9dGVzdDEgYXJnMj10ZXN0MgpDT05UQUlORVJfUkVHSVNUUklFU19DUkVERU5USUFMUz1leUpyT0hOVFpXTnlaWFJ6SWpwYmV5SmhkWFJvY3lJNmV5SnVkbU55TG1sdklqcDdJbUYxZEdnaU9pSmpNMUp1VEZkR2FWbDZSWGxOZW04eVdtcFplazVIVVRST1V6QTFUWHBHYkV4VVVYaGFhbGwwV1ZSS2JGbDVNREpPTWtrMFRucFZkMXBxUlhsT01sVTlJbjE5ZlYxOQpUQVNLX0NPTlRBSU5FUl9FTlY9VzNzaWEyVjVJam9pVkVGVFMxOUZUbFpmUzBWWklpd2lkbUZzZFdVaU9pSjBZWE5yWDNaaGJIVmxJbjFkClRBU0tfSEVBTFRIX0VORFBPSU5UPS92Mi9oZWFsdGgvcmVhZHkKVEFTS19IRUFMVEhfRVhQRUNURURfUkVTUE9OU0VfQ09ERT0iMjAwIgpUQVNLX0hFQUxUSF9QT1JUPSI1MDA1MSIKVEFTS19JRD0xM2UyYjU5OS05NmNhLTQyYjUtYTQxOS04ZmE3ZjcwMWQ1ZDIKVEFTS19OQU1FPW15LXRhc2sKVEFTS19QT1JUPSI1MDA1MSIKVEFTS19QUk9UT0NPTD1HUlBDClRBU0tfU0VDUkVUU19QUkVTRU5UPXRydWUKVEFTS19VUkw9L2dycGMKVEVSTUlOQVRJT05fR1JBQ0VfUEVSSU9EPVBUMkgKVFJBQ0lOR19BQ0NFU1NfVE9LRU49dHJhY2UtdG9rLTEKVVRJTFNfQ09OVEFJTkVSPW52Y3IuaW8vcXRmcHQxaDBiaWV1L252Y2YtY29yZS9udmNmX3dvcmtlcl91dGlsczoyLjIxLjQKSEVMTV9SRUdJU1RSSUVTX0NSRURFTlRJQUxTPWV5SnJPSE5UWldOeVpYUnpJanBiZXlKaGRYUm9jeUk2ZXlKb1pXeHRMbTVuWXk1dWRtbGthV0V1WTI5dElqcDdJbUYxZEdnaU9pSmpNMUp1VEZkR2FWbDZSWGxOZW04eVdtcFplazVIVVRST1V6QTFUWHBHYkV4VVVYaGFhbGwwV1ZSS2JGbDVNREpPTWtrMFRucFZkMXBxUlhsT01sVTlJbjE5ZlYxOQo=",
						CacheLaunchSpecification: &common.CacheLaunchSpecification{
							CacheArtifacts: true,
							CacheHandle:    "abc123handle",
							CacheSize:      262144000,
						},
						HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
							HelmChartURL: "https://foo/bar.tgz",
						},
					},
				},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			sr := &nvcav2beta1.ICMSRequest{}
			sr.Name = "sr-foo"
			sr.Spec = tt.icmsReqSpec

			gotPVC, gotJob, pullSecret, err := r.findAndDecodeCacheArtifacts(sr, "foo")
			if tt.expError != nil {
				assert.EqualError(t, err, tt.expError.Error())
			} else {
				require.NoError(t, err)
				assert.NotNil(t, gotPVC)
				assert.NotNil(t, gotJob)
				assert.NotNil(t, pullSecret)
			}
		})
	}
}

type mockCleanK8sClient struct {
	listFunc        func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
	deleteAllOfFunc func(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error
	deleteFunc      func(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error
	patchFunc       func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error
}

func (c *mockCleanK8sClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.listFunc(ctx, list, opts...)
}

func (c *mockCleanK8sClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return c.deleteAllOfFunc(ctx, obj, opts...)
}

func (c *mockCleanK8sClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return c.deleteFunc(ctx, obj, opts...)
}

func (c *mockCleanK8sClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return c.patchFunc(ctx, obj, patch, opts...)
}
