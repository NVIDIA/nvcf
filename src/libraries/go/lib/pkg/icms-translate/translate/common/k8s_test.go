/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func TestMergeEnvs(t *testing.T) {
	tests := []struct {
		name         string
		existingEnvs []corev1.EnvVar
		newEnvs      []corev1.EnvVar
		want         []corev1.EnvVar
		wantModified bool
	}{
		{
			name:         "empty existing envs",
			existingEnvs: []corev1.EnvVar{},
			newEnvs: []corev1.EnvVar{
				{Name: "FOO", Value: "bar"},
				{Name: "BAZ", Value: "qux"},
			},
			want: []corev1.EnvVar{
				{Name: "FOO", Value: "bar"},
				{Name: "BAZ", Value: "qux"},
			},
			wantModified: true,
		},
		{
			name: "no new envs",
			existingEnvs: []corev1.EnvVar{
				{Name: "FOO", Value: "bar"},
				{Name: "BAZ", Value: "qux"},
			},
			newEnvs: []corev1.EnvVar{},
			want: []corev1.EnvVar{
				{Name: "FOO", Value: "bar"},
				{Name: "BAZ", Value: "qux"},
			},
			wantModified: false,
		},
		{
			name: "update existing env",
			existingEnvs: []corev1.EnvVar{
				{Name: "FOO", Value: "old"},
				{Name: "BAZ", Value: "qux"},
			},
			newEnvs: []corev1.EnvVar{
				{Name: "FOO", Value: "new"},
			},
			want: []corev1.EnvVar{
				{Name: "FOO", Value: "new"},
				{Name: "BAZ", Value: "qux"},
			},
			wantModified: true,
		},
		{
			name: "update existing env ValueFrom to Value",
			existingEnvs: []corev1.EnvVar{
				{Name: "FOO", ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.name",
					},
				}},
			},
			newEnvs: []corev1.EnvVar{
				{Name: "FOO", Value: "new"},
			},
			want: []corev1.EnvVar{
				{Name: "FOO", Value: "new"},
			},
			wantModified: true,
		},
		{
			name: "update existing env ValueFrom to Value",
			existingEnvs: []corev1.EnvVar{
				{Name: "FOO", Value: "new"},
			},
			newEnvs: []corev1.EnvVar{
				{Name: "FOO", ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.name",
					},
				}},
			},
			want: []corev1.EnvVar{
				{Name: "FOO", ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.name",
					},
				}},
			},
			wantModified: true,
		},
		{
			name: "keep env ValueFrom on empty new value",
			existingEnvs: []corev1.EnvVar{
				{Name: "FOO", ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.name",
					},
				}},
			},
			newEnvs: []corev1.EnvVar{
				{Name: "FOO", Value: ""},
			},
			want: []corev1.EnvVar{
				{Name: "FOO", ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.name",
					},
				}},
			},
		},
		{
			name: "add new env",
			existingEnvs: []corev1.EnvVar{
				{Name: "FOO", Value: "bar"},
			},
			newEnvs: []corev1.EnvVar{
				{Name: "BAZ", Value: "qux"},
			},
			want: []corev1.EnvVar{
				{Name: "FOO", Value: "bar"},
				{Name: "BAZ", Value: "qux"},
			},
			wantModified: true,
		},
		{
			name: "no changes",
			existingEnvs: []corev1.EnvVar{
				{Name: "FOO", Value: "bar"},
				{Name: "BAZ", Value: "qux"},
			},
			newEnvs: []corev1.EnvVar{
				{Name: "FOO", Value: "bar"},
				{Name: "BAZ", Value: "qux"},
			},
			want: []corev1.EnvVar{
				{Name: "FOO", Value: "bar"},
				{Name: "BAZ", Value: "qux"},
			},
			wantModified: false,
		},
		{
			name: "multiple updates and additions",
			existingEnvs: []corev1.EnvVar{
				{Name: "FOO", Value: "old"},
				{Name: "BAZ", Value: "old"},
			},
			newEnvs: []corev1.EnvVar{
				{Name: "FOO", Value: "new"},
				{Name: "BAZ", Value: "new"},
				{Name: "QUX", Value: "new"},
			},
			want: []corev1.EnvVar{
				{Name: "FOO", Value: "new"},
				{Name: "BAZ", Value: "new"},
				{Name: "QUX", Value: "new"},
			},
			wantModified: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, modified := MergeEnvs(tt.existingEnvs, tt.newEnvs...)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantModified, modified)
		})
	}
}

func TestAddEnvsToContainer(t *testing.T) {
	tests := []struct {
		name      string
		container corev1.Container
		envs      []corev1.EnvVar
		want      bool
		wantEnvs  []corev1.EnvVar
	}{
		{
			name: "add new envs to empty container",
			container: corev1.Container{
				Name: "test-container",
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
				{Name: "ENV2", Value: "value2"},
			},
			want: true,
			wantEnvs: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
				{Name: "ENV2", Value: "value2"},
			},
		},
		{
			name: "update existing envs",
			container: corev1.Container{
				Name: "test-container",
				Env: []corev1.EnvVar{
					{Name: "ENV1", Value: "old-value1"},
					{Name: "ENV3", Value: "value3"},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "new-value1"},
				{Name: "ENV2", Value: "value2"},
			},
			want: true,
			wantEnvs: []corev1.EnvVar{
				{Name: "ENV1", Value: "new-value1"},
				{Name: "ENV3", Value: "value3"},
				{Name: "ENV2", Value: "value2"},
			},
		},
		{
			name: "no change when values match",
			container: corev1.Container{
				Name: "test-container",
				Env: []corev1.EnvVar{
					{Name: "ENV1", Value: "value1"},
					{Name: "ENV2", Value: "value2"},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
				{Name: "ENV2", Value: "value2"},
			},
			want: false,
			wantEnvs: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
				{Name: "ENV2", Value: "value2"},
			},
		},
		{
			name: "no envs to add",
			container: corev1.Container{
				Name: "test-container",
				Env: []corev1.EnvVar{
					{Name: "ENV1", Value: "value1"},
				},
			},
			envs: []corev1.EnvVar{},
			want: false,
			wantEnvs: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			container := tt.container
			got := AddEnvsToContainer(&container, tt.envs...)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantEnvs, container.Env)
		})
	}
}

func TestAddEnvsToContainers(t *testing.T) {
	tests := []struct {
		name           string
		containers     []corev1.Container
		envs           []corev1.EnvVar
		want           bool
		wantContainers []corev1.Container
	}{
		{
			name: "add to multiple containers",
			containers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "old-value1"},
					},
				},
				{
					Name: "container2",
					Env: []corev1.EnvVar{
						{Name: "ENV2", Value: "value2"},
					},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "new-value1"},
				{Name: "ENV3", Value: "value3"},
			},
			want: true,
			wantContainers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "new-value1"},
						{Name: "ENV3", Value: "value3"},
					},
				},
				{
					Name: "container2",
					Env: []corev1.EnvVar{
						{Name: "ENV2", Value: "value2"},
						{Name: "ENV1", Value: "new-value1"},
						{Name: "ENV3", Value: "value3"},
					},
				},
			},
		},
		{
			name: "no changes when all values match",
			containers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "value1"},
					},
				},
				{
					Name: "container2",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "value1"},
					},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
			},
			want: false,
			wantContainers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "value1"},
					},
				},
				{
					Name: "container2",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "value1"},
					},
				},
			},
		},
		{
			name:       "empty containers",
			containers: []corev1.Container{},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
			},
			want:           false,
			wantContainers: []corev1.Container{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			containers := make([]corev1.Container, len(tt.containers))
			copy(containers, tt.containers)
			got := AddEnvsToContainers(containers, tt.envs...)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantContainers, containers)
		})
	}
}

func TestAddEnvsToPod(t *testing.T) {
	tests := []struct {
		name    string
		pod     *corev1.Pod
		envs    []corev1.EnvVar
		want    bool
		wantPod *corev1.Pod
	}{
		{
			name: "add envs to pod containers",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "container1",
							Env: []corev1.EnvVar{
								{Name: "ENV1", Value: "old-value1"},
							},
						},
					},
					InitContainers: []corev1.Container{
						{
							Name: "init-container",
							Env: []corev1.EnvVar{
								{Name: "INIT_ENV", Value: "init-value"},
							},
						},
					},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "new-value1"},
				{Name: "ENV2", Value: "value2"},
			},
			want: true,
			wantPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "container1",
							Env: []corev1.EnvVar{
								{Name: "ENV1", Value: "new-value1"},
								{Name: "ENV2", Value: "value2"},
							},
						},
					},
					InitContainers: []corev1.Container{
						{
							Name: "init-container",
							Env: []corev1.EnvVar{
								{Name: "INIT_ENV", Value: "init-value"},
								{Name: "ENV1", Value: "new-value1"},
								{Name: "ENV2", Value: "value2"},
							},
						},
					},
				},
			},
		},
		{
			name: "no change when values match",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "container1",
							Env: []corev1.EnvVar{
								{Name: "ENV1", Value: "value1"},
							},
						},
					},
					InitContainers: []corev1.Container{
						{
							Name: "init-container",
							Env: []corev1.EnvVar{
								{Name: "ENV1", Value: "value1"},
							},
						},
					},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
			},
			want: false,
			wantPod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "container1",
							Env: []corev1.EnvVar{
								{Name: "ENV1", Value: "value1"},
							},
						},
					},
					InitContainers: []corev1.Container{
						{
							Name: "init-container",
							Env: []corev1.EnvVar{
								{Name: "ENV1", Value: "value1"},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := tt.pod.DeepCopy()
			got := AddEnvsToPod(pod, tt.envs...)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantPod, pod)
		})
	}
}

func TestAddOptionalEnvsToContainers(t *testing.T) {
	tests := []struct {
		name           string
		containers     []corev1.Container
		envs           []corev1.EnvVar
		want           bool
		wantContainers []corev1.Container
	}{
		{
			name: "add new envs without replacing existing",
			containers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "existing-value1"},
						{Name: "ENV2", Value: "existing-value2"},
					},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "new-value1"},
				{Name: "ENV3", Value: "new-value3"},
			},
			want: true,
			wantContainers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "existing-value1"},
						{Name: "ENV2", Value: "existing-value2"},
						{Name: "ENV3", Value: "new-value3"},
					},
				},
			},
		},
		{
			name: "add to multiple containers without replacing",
			containers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "value1"},
					},
				},
				{
					Name: "container2",
					Env: []corev1.EnvVar{
						{Name: "ENV2", Value: "value2"},
					},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "new-value1"},
				{Name: "ENV3", Value: "value3"},
			},
			want: true,
			wantContainers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "value1"},
						{Name: "ENV3", Value: "value3"},
					},
				},
				{
					Name: "container2",
					Env: []corev1.EnvVar{
						{Name: "ENV2", Value: "value2"},
						{Name: "ENV1", Value: "new-value1"},
						{Name: "ENV3", Value: "value3"},
					},
				},
			},
		},
		{
			name: "no changes when all envs already exist",
			containers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "value1"},
						{Name: "ENV2", Value: "value2"},
					},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "different-value"},
				{Name: "ENV2", Value: "another-value"},
			},
			want: false,
			wantContainers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "value1"},
						{Name: "ENV2", Value: "value2"},
					},
				},
			},
		},
		{
			name: "add to empty containers",
			containers: []corev1.Container{
				{
					Name: "container1",
					Env:  []corev1.EnvVar{},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "value1"},
			},
			want: true,
			wantContainers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "value1"},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			containers := make([]corev1.Container, len(tt.containers))
			copy(containers, tt.containers)
			got := AddOptionalEnvsToContainers(containers, tt.envs...)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantContainers, containers)
		})
	}
}

func TestAddMixedEnvsToContainers(t *testing.T) {
	tests := []struct {
		name             string
		keepExistingEnvs map[string]bool
		containers       []corev1.Container
		envs             []corev1.EnvVar
		want             bool
		wantContainers   []corev1.Container
	}{
		{
			name:             "replace all envs when keepExistingEnvs is empty",
			keepExistingEnvs: map[string]bool{},
			containers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "old-value1"},
						{Name: "ENV2", Value: "old-value2"},
					},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "new-value1"},
				{Name: "ENV2", Value: "new-value2"},
			},
			want: true,
			wantContainers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "new-value1"},
						{Name: "ENV2", Value: "new-value2"},
					},
				},
			},
		},
		{
			name: "keep specific envs from replacement",
			keepExistingEnvs: map[string]bool{
				"ENV1": true,
			},
			containers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "keep-value1"},
						{Name: "ENV2", Value: "old-value2"},
						{Name: "ENV3", Value: "old-value3"},
					},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "new-value1"},
				{Name: "ENV2", Value: "new-value2"},
				{Name: "ENV4", Value: "new-value4"},
			},
			want: true,
			wantContainers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "keep-value1"},
						{Name: "ENV2", Value: "new-value2"},
						{Name: "ENV3", Value: "old-value3"},
						{Name: "ENV4", Value: "new-value4"},
					},
				},
			},
		},
		{
			name: "keep multiple specific envs",
			keepExistingEnvs: map[string]bool{
				"ENV1": true,
				"ENV3": true,
			},
			containers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "keep-value1"},
						{Name: "ENV2", Value: "old-value2"},
						{Name: "ENV3", Value: "keep-value3"},
					},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "new-value1"},
				{Name: "ENV2", Value: "new-value2"},
				{Name: "ENV3", Value: "new-value3"},
			},
			want: true,
			wantContainers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "keep-value1"},
						{Name: "ENV2", Value: "new-value2"},
						{Name: "ENV3", Value: "keep-value3"},
					},
				},
			},
		},
		{
			name: "add new envs to multiple containers with selective keeping",
			keepExistingEnvs: map[string]bool{
				"ENV2": true,
			},
			containers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "old-value1"},
						{Name: "ENV2", Value: "keep-value2"},
					},
				},
				{
					Name: "container2",
					Env: []corev1.EnvVar{
						{Name: "ENV2", Value: "keep-value2-c2"},
					},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "new-value1"},
				{Name: "ENV2", Value: "new-value2"},
				{Name: "ENV3", Value: "new-value3"},
			},
			want: true,
			wantContainers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "new-value1"},
						{Name: "ENV2", Value: "keep-value2"},
						{Name: "ENV3", Value: "new-value3"},
					},
				},
				{
					Name: "container2",
					Env: []corev1.EnvVar{
						{Name: "ENV2", Value: "keep-value2-c2"},
						{Name: "ENV1", Value: "new-value1"},
						{Name: "ENV3", Value: "new-value3"},
					},
				},
			},
		},
		{
			name:             "nil keepExistingEnvs replaces all",
			keepExistingEnvs: nil,
			containers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "old-value1"},
					},
				},
			},
			envs: []corev1.EnvVar{
				{Name: "ENV1", Value: "new-value1"},
			},
			want: true,
			wantContainers: []corev1.Container{
				{
					Name: "container1",
					Env: []corev1.EnvVar{
						{Name: "ENV1", Value: "new-value1"},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			containers := make([]corev1.Container, len(tt.containers))
			copy(containers, tt.containers)
			got := AddMixedEnvsToContainers(tt.keepExistingEnvs, containers, tt.envs...)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantContainers, containers)
		})
	}
}
