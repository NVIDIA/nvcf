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
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(nvcav2beta1.AddToScheme(s))
	utilruntime.Must(nvcav1new.AddToScheme(s))
	utilruntime.Must(corev1.AddToScheme(s))
	utilruntime.Must(appsv1.AddToScheme(s))
	utilruntime.Must(coordv1.AddToScheme(s))
	utilruntime.Must(storagev1.AddToScheme(s))
	utilruntime.Must(batchv1.AddToScheme(s))
	return s
}

func newTestRESTMapper(s *runtime.Scheme) meta.RESTMapper {
	gvs := s.PreferredVersionAllGroups()
	rm := meta.NewDefaultRESTMapper(gvs)
	for gvk := range s.AllKnownTypes() {
		scope := meta.RESTScopeNamespace
		switch gvk.Kind {
		case "PersistentVolume", "StorageClass":
			scope = meta.RESTScopeRoot
		}
		rm.Add(gvk, scope)
	}
	return rm
}

func newFakeClient(s *runtime.Scheme, objs ...client.Object) client.Client {
	return clientfake.NewClientBuilder().
		WithScheme(s).
		WithRESTMapper(newTestRESTMapper(s)).
		WithObjects(objs...).
		WithStatusSubresource(&nvcav1new.StorageRequest{}).
		Build()
}

func newBool(v bool) *bool       { return &v }
func newInt32(v int32) *int32    { return &v }
func newString(v string) *string { return &v }

func mergeMaps(m1, m2 map[string]string) map[string]string {
	merged := make(map[string]string)
	for k, v := range m1 {
		merged[k] = v
	}
	for key, value := range m2 {
		merged[key] = value
	}
	return merged
}

func TestHelperAPIs(t *testing.T) {
	r := &Reconciler{ICMSRequestNamespace: nvcatypes.DefaultICMSRequestNamespace}

	stReq := nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stor-req",
			Namespace: nvcatypes.DefaultICMSRequestNamespace,
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			ICMSRequestName: "random-req",
			Type:            nvcav1new.SharedStorageRequest,
			SharedStorage: &nvcav1new.SharedStorageSpec{
				SMBContainerImage:    "smb:latest",
				WorkerPullSecretName: "foo-worker",
			},
		},
	}
	err := r.validateStorageRequest(&stReq, nil)
	assert.Nil(t, err)

	stReq = nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stor-req",
			Namespace: nvcatypes.DefaultICMSRequestNamespace,
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			ICMSRequestName: "random-req",
			Type:            nvcav1new.InternalPersistentStorageRequest,
			InternalPersistentStorage: &nvcav1new.InternalPersistentStorageSpec{
				StorageClassName: "random",
			},
		},
	}
	err = r.validateStorageRequest(&stReq, nil)
	assert.Nil(t, err)

	// Instance namespace fallback: request name empty but namespace = instance ns name
	stReq = nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared-storage",
			Namespace: "sr-275d0ad9-f998-4abc-8bb0-75772343854b",
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type: nvcav1new.SharedStorageRequest,
			SharedStorage: &nvcav1new.SharedStorageSpec{
				SMBContainerImage:    "smb:latest",
				WorkerPullSecretName: "foo-worker",
			},
		},
	}
	err = r.validateStorageRequest(&stReq, nil)
	assert.Nil(t, err)

	stReq = nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stor-req",
			Namespace: nvcatypes.DefaultICMSRequestNamespace,
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			ICMSRequestName: "random-req",
			Type:            nvcav1new.ModelCacheRequest,
			ModelCache: &nvcav1new.ModelCacheSpec{
				Encryption: &nvcav1new.ModelCacheEncryption{
					Required: true,
				},
			},
			InternalPersistentStorage: &nvcav1new.InternalPersistentStorageSpec{
				StorageClassName: "random",
			},
		},
	}
	err = r.validateStorageRequest(&stReq, nil)
	assert.NotNil(t, err)

	stReq = nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stor-req",
			Namespace: nvcatypes.DefaultICMSRequestNamespace,
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type: nvcav1new.ModelCacheRequest,
			ModelCache: &nvcav1new.ModelCacheSpec{
				Encryption: &nvcav1new.ModelCacheEncryption{
					Required: true,
				},
			},
		},
	}
	err = r.validateStorageRequest(&stReq, nil)
	assert.NotNil(t, err)

	stReq = nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stor-req",
			Namespace: nvcatypes.DefaultICMSRequestNamespace,
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type: nvcav1new.ModelCacheRequest,
		},
	}
	err = r.validateStorageRequest(&stReq, nil)
	assert.NotNil(t, err)

	stReq = nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stor-req",
			Namespace: nvcatypes.DefaultICMSRequestNamespace,
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type: nvcav1new.InternalPersistentStorageRequest,
		},
	}
	err = r.validateStorageRequest(&stReq, nil)
	assert.NotNil(t, err)

	stReq = nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stor-req",
			Namespace: nvcatypes.DefaultICMSRequestNamespace,
			Labels: map[string]string{
				"function-version-id": "random-id",
			},
		},
		Spec: nvcav1new.StorageRequestSpec{
			Type: nvcav1new.SharedStorageRequest,
		},
	}
	err = r.validateStorageRequest(&stReq, nil)
	assert.NotNil(t, err)
}
