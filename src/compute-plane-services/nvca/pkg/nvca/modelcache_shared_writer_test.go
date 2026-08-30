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

package nvca

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

func TestPrepareRWXReadOnlySharedWriterJobCanonicalizesMetadata(t *testing.T) {
	fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
	fixture.job.Labels["request.example/label"] = "request-a"
	fixture.job.Annotations = map[string]string{"request.example/annotation": "request-a"}
	fixture.job.Spec.Template.Labels["request.example/label"] = "request-a"
	fixture.job.Spec.Template.Annotations = map[string]string{}
	fixture.job.Spec.Template.Annotations["request.example/annotation"] = "request-a"

	require.NoError(t, fixture.backend.prepareRegularModelCacheBindingResources(
		t.Context(), fixture.binding, fixture.rwPVC, fixture.job))

	assert.Equal(t, map[string]string{
		nvcastorage.ModelCacheBindingUIDLabelKey: string(fixture.binding.UID),
	}, fixture.job.Labels)
	assert.Empty(t, fixture.job.Annotations)
	assert.Equal(t, map[string]string{
		nvcastorage.ModelCacheBindingUIDLabelKey: string(fixture.binding.UID),
	}, fixture.job.Spec.Template.Labels)
	assert.Empty(t, fixture.job.Spec.Template.Annotations)
	require.NotNil(t, fixture.job.Spec.Template.Spec.AutomountServiceAccountToken)
	assert.False(t, *fixture.job.Spec.Template.Spec.AutomountServiceAccountToken)
}

func TestPrepareRWXReadOnlySharedWriterJobRejectsRequestScopedInputs(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*rwxReadOnlyRuntimeFixture)
		wantErr string
	}{
		{
			name: "raw worker token",
			mutate: func(fixture *rwxReadOnlyRuntimeFixture) {
				fixture.job.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{
					Name: "NVCF_WORKER_TOKEN", Value: "request-token",
				}}
			},
			wantErr: "retains environment input",
		},
		{
			name: "environment Secret reference",
			mutate: func(fixture *rwxReadOnlyRuntimeFixture) {
				fixture.job.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{
					Name: "NVCF_WORKER_TOKEN",
					ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "request-secret"},
						Key:                  "token",
					}},
				}}
			},
			wantErr: "binding-scoped credential indirection",
		},
		{
			name: "environment source",
			mutate: func(fixture *rwxReadOnlyRuntimeFixture) {
				fixture.job.Spec.Template.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{
					SecretRef: &corev1.SecretEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "request-secret"},
					},
				}}
			},
			wantErr: "immutable input identity",
		},
		{
			name: "image pull Secret",
			mutate: func(fixture *rwxReadOnlyRuntimeFixture) {
				fixture.job.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{
					Name: "request-pull-secret",
				}}
			},
			wantErr: "binding-scoped pull Secret identity and cleanup are not implemented",
		},
		{
			name: "Secret volume",
			mutate: func(fixture *rwxReadOnlyRuntimeFixture) {
				fixture.job.Spec.Template.Spec.Volumes = append(
					fixture.job.Spec.Template.Spec.Volumes,
					corev1.Volume{
						Name: "request-secret",
						VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
							SecretName: "request-secret",
						}},
					})
			},
			wantErr: "binding-scoped Secret identity and cleanup are not implemented",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRWXReadOnlyRuntimeFixture(t, "weka", "csi.weka.io")
			fixture.job.Labels = map[string]string{}
			fixture.job.Spec.Template.Labels = map[string]string{}
			tt.mutate(fixture)

			err := fixture.backend.prepareRegularModelCacheBindingResources(
				t.Context(), fixture.binding, fixture.rwPVC, fixture.job)
			require.ErrorContains(t, err, tt.wantErr)
			assert.True(t, nvcaerrors.IsTerminal(err))
		})
	}
}
