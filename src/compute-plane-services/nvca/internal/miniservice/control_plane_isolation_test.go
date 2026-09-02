/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package mscontroller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestMiniServiceEventHandlerScopesControlPlane(t *testing.T) {
	owned := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
		miniserviceNameLabel:          "plane-a-request-miniservice",
		nvcatypes.ControlPlaneIDLabel: "plane-a",
	}}}
	foreign := owned.DeepCopy()
	foreign.Labels[nvcatypes.ControlPlaneIDLabel] = "plane-b"

	assert.Len(t, miniServiceRequestsForObject(owned, "plane-a"), 1)
	assert.Empty(t, miniServiceRequestsForObject(foreign, "plane-a"))
}

func TestControlPlaneObjectPredicate(t *testing.T) {
	p := controlPlaneObjectPredicate("plane-a")
	owned := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
		nvcatypes.ControlPlaneIDLabel: "plane-a",
	}}}
	foreign := owned.DeepCopy()
	foreign.Labels[nvcatypes.ControlPlaneIDLabel] = "plane-b"

	assert.True(t, p(client.Object(owned)))
	assert.False(t, p(client.Object(foreign)))
	assert.True(t, controlPlaneObjectPredicate("")(client.Object(foreign)))
}
