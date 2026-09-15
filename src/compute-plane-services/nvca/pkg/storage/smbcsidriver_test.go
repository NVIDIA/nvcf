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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	storagev1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type mockCSIDriverGetter struct {
	driver *storagev1.CSIDriver
	err    error
}

func (m *mockCSIDriverGetter) Get(ctx context.Context, name string, opts metav1.GetOptions) (*storagev1.CSIDriver, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.driver, nil
}

func TestNewSMBCSIDriverHealthCheck(t *testing.T) {
	mockGetter := &mockCSIDriverGetter{}
	check := NewSMBCSIDriverHealthCheck(mockGetter)
	assert.NotNil(t, check)
	assert.Equal(t, mockGetter, check.csiDriverGetter)
}

func TestGetComponentStatus_Healthy(t *testing.T) {
	mockGetter := &mockCSIDriverGetter{
		driver: &storagev1.CSIDriver{
			ObjectMeta: metav1.ObjectMeta{
				Name: SMBCSIDriverName,
			},
		},
	}

	check := NewSMBCSIDriverHealthCheck(mockGetter)
	health, err := check.GetComponentStatus(context.Background())

	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, health.Status)
	assert.Equal(t, nvcatypes.HealthStatusHealthy, health.Components[SMBCSIDriverName].Status)
}

func TestGetComponentStatus_NotFound(t *testing.T) {
	mockGetter := &mockCSIDriverGetter{
		err: k8serrors.NewNotFound(storagev1.Resource("csidriver"), SMBCSIDriverName),
	}

	check := NewSMBCSIDriverHealthCheck(mockGetter)
	health, err := check.GetComponentStatus(context.Background())

	require.NoError(t, err)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, health.Status)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, health.Components[SMBCSIDriverName].Status)
	assert.Len(t, health.Components[SMBCSIDriverName].Errors, 1)
	assert.Contains(t, health.Components[SMBCSIDriverName].Errors[0], SMBCSIDriverName)
	assert.Contains(t, health.Components[SMBCSIDriverName].Errors[0], "must be installed")
}

func TestGetComponentStatus_Error(t *testing.T) {
	mockGetter := &mockCSIDriverGetter{
		err: errors.New("some error"),
	}

	check := NewSMBCSIDriverHealthCheck(mockGetter)
	health, err := check.GetComponentStatus(context.Background())

	require.Error(t, err)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, health.Status)
	assert.Equal(t, nvcatypes.HealthStatusUnhealthy, health.Components[SMBCSIDriverName].Status)
	assert.Len(t, health.Components[SMBCSIDriverName].Errors, 1)
	assert.Equal(t, "some error", health.Components[SMBCSIDriverName].Errors[0])
}

func TestFmtSMBCSIDriverNotFoundError(t *testing.T) {
	originalErr := errors.New("not found")
	err := fmtSMBCSIDriverNotFoundError(originalErr)

	assert.Contains(t, err.Error(), SMBCSIDriverName)
	assert.Contains(t, err.Error(), "must be installed")
	assert.Contains(t, err.Error(), "feature flag is enabled")
	assert.Contains(t, err.Error(), "https://github.com/kubernetes-csi/csi-driver-smb")
}
