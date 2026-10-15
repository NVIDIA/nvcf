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

package k8sutil

import (
	"math"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAddTaskCleanupGracePeriod(t *testing.T) {
	tests := []struct {
		name               string
		maxRuntimeDuration time.Duration
		expected           time.Duration
	}{
		{
			name:               "adds cleanup grace period",
			maxRuntimeDuration: time.Hour,
			expected:           time.Hour + TaskCleanupExtraGracePeriod,
		},
		{
			name:               "saturates unbounded max runtime duration",
			maxRuntimeDuration: time.Duration(math.MaxInt64),
			expected:           time.Duration(math.MaxInt64),
		},
		{
			name:               "saturates near max runtime duration",
			maxRuntimeDuration: time.Duration(math.MaxInt64) - time.Minute,
			expected:           time.Duration(math.MaxInt64),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, AddTaskCleanupGracePeriod(tt.maxRuntimeDuration))
		})
	}
}

func TestFindNVCFWorkerImagePullSecretObjects(t *testing.T) {
	type spec struct {
		name      string
		objs      []metav1.Object
		expWorker []*corev1.Secret
		expErr    string
	}

	cases := []spec{
		{
			name: "worker only (ECR IAM case)",
			objs: []metav1.Object{
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "worker-foo"}},
			},
			expWorker: []*corev1.Secret{
				{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "worker-foo"}},
			},
		},
		{
			name: "worker and workload both present",
			objs: []metav1.Object{
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "worker-foo"}},
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "foo-core"}},
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "workload-foo"}},
			},
			expWorker: []*corev1.Secret{
				{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "worker-foo"}},
				{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "foo-core"}},
			},
		},
		{
			name: "ignore non docker cfg json",
			objs: []metav1.Object{
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "worker-foo"}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "worker-bar"}},
			},
			expWorker: []*corev1.Secret{
				{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "worker-foo"}},
			},
		},
		{
			name:   "no secrets",
			objs:   []metav1.Object{},
			expErr: "no worker image pull secret found",
		},
		{
			name: "workload only — no worker",
			objs: []metav1.Object{
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "workload-foo"}},
			},
			expErr: "no worker image pull secret found",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotWorkerSecrets, err := FindNVCFWorkerImagePullSecretObjects(tt.objs...)
			if tt.expErr != "" {
				assert.EqualError(t, err, tt.expErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expWorker, gotWorkerSecrets)
			}
		})
	}
}

func TestFindNVCFImagePullSecretObjects(t *testing.T) {
	type spec struct {
		name        string
		objs        []metav1.Object
		expWorker   []*corev1.Secret
		expWorkload []*corev1.Secret
		expErr      string
	}

	cases := []spec{
		{
			name: "found",
			objs: []metav1.Object{
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "worker-foo"}},
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "foo-worker"}},
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "foo-core"}},
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "workload-foo"}},
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "foo-inference"}},
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "foo-task"}},
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: common.HelmWorkloadPullSecretName}},
			},
			expWorker: []*corev1.Secret{
				{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "worker-foo"}},
				{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "foo-worker"}},
				{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "foo-core"}},
			},
			expWorkload: []*corev1.Secret{
				{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "workload-foo"}},
				{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "foo-inference"}},
				{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "foo-task"}},
				{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: common.HelmWorkloadPullSecretName}},
			},
		},
		{
			name: "ignore non docker cfg json",
			objs: []metav1.Object{
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "worker-foo"}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "worker-bar"}},
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "workload-foo"}},
			},
			expWorker: []*corev1.Secret{
				{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "worker-foo"}},
			},
			expWorkload: []*corev1.Secret{
				{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "workload-foo"}},
			},
		},
		{
			name: "no worker",
			objs: []metav1.Object{
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "workload-foo"}},
			},
			expErr: "no worker image pull secret found",
		},
		{
			name: "no workload",
			objs: []metav1.Object{
				&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, ObjectMeta: metav1.ObjectMeta{Name: "worker-foo"}},
			},
			expErr: "no workload image pull secret found",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotWorkerSecrets, gotWorkloadSecrets, err := FindNVCFImagePullSecretObjects(tt.objs...)
			if tt.expErr != "" {
				assert.EqualError(t, err, tt.expErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expWorker, gotWorkerSecrets)
				assert.Equal(t, tt.expWorkload, gotWorkloadSecrets)
			}
		})
	}
}
