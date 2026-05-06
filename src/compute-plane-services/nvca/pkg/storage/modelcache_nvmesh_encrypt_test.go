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
	"crypto/rand"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	nvcaotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	nvcav1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func Test_generateToken(t *testing.T) {
	ctx := context.Background()
	assert.NotEmpty(t, generateToken(ctx, rand.Reader, "random"))
}

func TestSecretSetupHelpers(t *testing.T) {
	ctx := context.Background()

	// create fake kubernetes client
	k8sClient := fake.NewFakeClient(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ModelCacheInitNamespace},
		},
	)

	// call the reconciler manually as the setup is a mock
	r := &Reconciler{
		Client:     k8sClient,
		nowFunc:    time.Now,
		randReader: rand.Reader,
		tracer:     nvcaotel.NewTracer(),
		metrics:    newTestMetrics(),
	}

	st := &nvcav1.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: nvcav1.SharedStorageRequest.Name(),
			Labels: nvcatypes.GetLabelsForRequest(&nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-req",
				},
				Spec: nvcav2beta1.ICMSRequestSpec{
					NCAId: "nca-id",
					FunctionDetails: function.Details{
						FunctionID:        "func-id",
						FunctionVersionID: "func-version-id",
					},
				},
			}, &mockFetcher{}),
			Annotations: nvcatypes.GetAnnotationsForRequest(&nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-req",
				},
				Spec: nvcav2beta1.ICMSRequestSpec{
					NCAId: "nca-id",
					FunctionDetails: function.Details{
						FunctionID:        "func-id",
						FunctionVersionID: "func-version-id",
					},
				},
			}),
		},
		Spec: nvcav1.StorageRequestSpec{
			Type:            nvcav1.SharedStorageRequest,
			ICMSRequestName: "test-req",
			SharedStorage: &nvcav1.SharedStorageSpec{
				SMBContainerImage:    "smb:latest",
				WorkerPullSecretName: "foo-worker",
				Size:                 *resource.NewQuantity(1<<20, resource.BinarySI),
			},
		},
	}

	const ncaHash = "b5c1d739f56394ac081eaada05d22bc9"
	scName, err := r.doEncryptedStorageClassNVMesh(ctx, st, "nca-id")
	require.NoError(t, err)
	require.Equal(t, "sc-"+ncaHash, scName)

	expSecretKey := client.ObjectKey{Name: "scsec-" + ncaHash, Namespace: ModelCacheInitNamespace}
	err = k8sClient.Get(ctx, expSecretKey, &corev1.Secret{})
	require.NoError(t, err)

	err = k8sClient.Get(ctx, client.ObjectKey{Name: scName}, &storagev1.StorageClass{})
	require.NoError(t, err)
}
