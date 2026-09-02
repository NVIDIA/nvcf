/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvcav1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestStorageRequestPredicateScopesNamespaceAndControlPlane(t *testing.T) {
	filter := filterStorageRequest(nvcav1.SharedStorageRequest, "plane-a-nvcf-backend", "plane-a")
	owned := &nvcav1.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{Namespace: "plane-a-nvcf-backend", Labels: map[string]string{
			nvcatypes.ControlPlaneIDLabel: "plane-a",
		}},
		Spec: nvcav1.StorageRequestSpec{Type: nvcav1.SharedStorageRequest},
	}
	foreign := owned.DeepCopy()
	foreign.Labels[nvcatypes.ControlPlaneIDLabel] = "plane-b"
	wrongNamespace := owned.DeepCopy()
	wrongNamespace.Namespace = "plane-b-nvcf-backend"

	assert.True(t, filter(owned))
	assert.False(t, filter(foreign))
	assert.False(t, filter(wrongNamespace))
}

func TestModelCacheDisabledForNamedControlPlane(t *testing.T) {
	assert.Equal(t, []nvcav1.StorageRequestType{
		nvcav1.SharedStorageRequest,
		nvcav1.InternalPersistentStorageRequest,
	}, ControllerTypes(true, "plane-a"))
	assert.Contains(t, ControllerTypes(true, ""), nvcav1.ModelCacheRequest)
}
