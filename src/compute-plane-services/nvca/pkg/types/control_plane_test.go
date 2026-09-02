/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

func TestValidateControlPlaneID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "legacy empty", id: ""},
		{name: "named", id: "plane-a"},
		{name: "reserved default", id: "default", wantErr: true},
		{name: "uppercase", id: "Plane-A", wantErr: true},
		{name: "too long", id: "control-plane-id-over-twenty", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateControlPlaneID(tt.id)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestGetLabelsForRequestPreservesControlPlaneIdentity(t *testing.T) {
	req := &nvcav2beta1.ICMSRequest{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
		ControlPlaneIDLabel: "plane-a",
	}}}
	assert.Equal(t, "plane-a", GetLabelsForRequest(req, nil)[ControlPlaneIDLabel])
}

func TestControlPlaneResourceNames(t *testing.T) {
	assert.Equal(t, "nvca-system", ControlPlaneResourceName("", "nvca-system"))
	assert.Equal(t, "plane-a-nvca-system", ControlPlaneResourceName("plane-a", "nvca-system"))
	assert.Equal(t, "plane-a-sr-request", ControlPlaneResourceName("plane-a", "sr-request"))

	long := ControlPlaneResourceName("plane-a", "sr-abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwxyz-123")
	require.LessOrEqual(t, len(long), 63)
	assert.Equal(t, long, ControlPlaneResourceName("plane-a", "sr-abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwxyz-123"))
	assert.NotEqual(t, long, ControlPlaneResourceName("plane-b", "sr-abcdefghijklmnopqrstuvwxyz-abcdefghijklmnopqrstuvwxyz-123"))
}

func TestIsOwnedByControlPlane(t *testing.T) {
	owned := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{ControlPlaneIDLabel: "plane-a"},
	}}
	unlabelled := &metav1.PartialObjectMetadata{}

	assert.True(t, IsOwnedByControlPlane(owned, "plane-a"))
	assert.False(t, IsOwnedByControlPlane(owned, "plane-b"))
	assert.False(t, IsOwnedByControlPlane(unlabelled, "plane-a"))
	assert.True(t, IsOwnedByControlPlane(unlabelled, ""), "legacy mode preserves existing behavior")
}
