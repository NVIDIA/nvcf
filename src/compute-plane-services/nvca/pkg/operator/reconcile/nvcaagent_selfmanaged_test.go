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

package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

func TestDetectIdentitySource(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		want    string
		wantErr bool
	}{
		{name: "empty defaults to psat", mode: "", want: "psat"},
		{name: "psat explicit", mode: "psat", want: "psat"},
		// SPIRE scaffolding remains in-tree but is not yet supported end-to-end,
		// so it must be rejected here rather than silently routing through the
		// SPIRE branch.
		{name: "spire rejected (not yet supported)", mode: "spire", wantErr: true},
		// Vault and auto are no longer valid for self-hosted clusters.
		{name: "vault rejected", mode: "vault", wantErr: true},
		{name: "auto rejected", mode: "auto", wantErr: true},
		{name: "unknown rejected", mode: "kerberos", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := detectIdentitySource(tc.mode)
			if tc.wantErr {
				assert.Error(t, err, "want error for mode=%q", tc.mode)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// agentDeployment returns a minimal Deployment with a single "agent" container
// — the shape applyPSATIdentity / applySPIREIdentity expect.
func agentDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "agent"}},
				},
			},
		},
	}
}

func TestApplySelfManagedNVCADeployment_RejectsInvalidSource(t *testing.T) {
	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "test"},
		Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
			ClusterConfig: nvidiaiov1.ClusterConfig{ClusterID: "abc", ClusterName: "byoc-test"},
		}},
	}
	err := applySelfManagedNVCADeployment(context.Background(), nb, agentDeployment(), nil, "vault", "registry/nvca:v1")
	require.Error(t, err, "vault is no longer valid for self-hosted")
	assert.Contains(t, err.Error(), "invalid identity-source")
}
