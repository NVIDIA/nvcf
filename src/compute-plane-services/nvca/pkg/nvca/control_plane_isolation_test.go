/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package nvca

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestInstanceNamespaceSelectorScopesControlPlane(t *testing.T) {
	selector, err := instanceNamespaceSelector("plane-a")
	assert.NoError(t, err)
	assert.True(t, selector.Matches(labels.Set{
		nvcatypes.WorkloadInstanceTypeLabel: "miniservice",
		nvcatypes.ControlPlaneIDLabel:       "plane-a",
	}))
	assert.False(t, selector.Matches(labels.Set{
		nvcatypes.WorkloadInstanceTypeLabel: "miniservice",
		nvcatypes.ControlPlaneIDLabel:       "plane-b",
	}))
	assert.True(t, legacyModelCacheResourcesEnabled(""))
	assert.False(t, legacyModelCacheResourcesEnabled("plane-a"))
}

func TestAgentBackendCacheControlPlaneIsolation(t *testing.T) {
	b := NewBackendk8sCacheBuilder().
		WithSystemNamespace("plane-a-nvca-system").
		WithRequestsNamespace("plane-a-nvcf-backend").
		WithControlPlaneID("plane-a")

	assert.Equal(t, "plane-a", b.controlPlaneID)
	assert.Equal(t, "plane-a-nvca-system", b.systemNamespace)
	assert.Equal(t, "plane-a-nvcf-backend", b.requestsNamespace)
}

func TestMiniServiceIdentityNames(t *testing.T) {
	assert.Equal(t, "sr-request-miniservice", getMiniServiceInstanceID("sr-request"))
	assert.Equal(t, "plane-a-sr-request-miniservice", getMiniServiceInstanceID("sr-request", "plane-a"))
	assert.Equal(t, "sr-request", getMiniServiceNamespace("sr-request"))
	assert.Equal(t, "plane-a-sr-request", getMiniServiceNamespace("sr-request", "plane-a"))
}

func TestICMSRequestOwnership(t *testing.T) {
	request := &nvcav2beta1.ICMSRequest{ObjectMeta: metav1.ObjectMeta{
		Namespace: "plane-a-nvcf-backend",
		Labels:    map[string]string{nvcatypes.ControlPlaneIDLabel: "plane-a"},
	}}
	assert.True(t, ownsICMSRequest(request, "plane-a-nvcf-backend", "plane-a"))
	assert.False(t, ownsICMSRequest(request, "plane-b-nvcf-backend", "plane-a"))
	assert.False(t, ownsICMSRequest(request, "plane-a-nvcf-backend", "plane-b"))
	assert.True(t, ownsICMSRequest(request, "", ""), "legacy mode remains unfiltered")
}
