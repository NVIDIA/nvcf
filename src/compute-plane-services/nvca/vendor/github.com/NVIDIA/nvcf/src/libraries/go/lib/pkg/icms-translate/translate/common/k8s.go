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
	"slices"

	corev1 "k8s.io/api/core/v1"
)

func AddEnvsToContainers(cs []corev1.Container, envs ...corev1.EnvVar) bool {
	mod := false
	for i := range cs {
		added := AddEnvsToContainer(&cs[i], envs...)
		mod = mod || added
	}
	return mod
}

// AddOptionalEnvsToContainers adds the new envs to the containers. Existing envs aren't
// replaced by the new envs.
func AddOptionalEnvsToContainers(cs []corev1.Container, envs ...corev1.EnvVar) bool {
	mod := false
	for i := range cs {
		added := addOptionalEnvsToContainer(true, nil, &cs[i], envs...)
		mod = mod || added
	}
	return mod
}

// AddMixedEnvsToContainers adds the new envs to the containers. Existing envs are
// replaced by the new envs unless they are in keepExistingEnvVars.
func AddMixedEnvsToContainers(keepExistingEnvVars map[string]bool, cs []corev1.Container, envs ...corev1.EnvVar) bool {
	mod := false
	for i := range cs {
		added := addOptionalEnvsToContainer(false, keepExistingEnvVars, &cs[i], envs...)
		mod = mod || added
	}
	return mod
}

func AddEnvsToContainer(c *corev1.Container, inEnvs ...corev1.EnvVar) bool {
	envs, ok := MergeEnvs(c.Env, inEnvs...)
	c.Env = envs
	return ok
}

func AddEnvsToPod(pod *corev1.Pod, envs ...corev1.EnvVar) bool {
	mod := false
	// Add envs to all containers and initContainers in the pod
	modContainers := AddEnvsToContainers(pod.Spec.Containers, envs...)
	modInitContainers := AddEnvsToContainers(pod.Spec.InitContainers, envs...)
	mod = mod || modContainers || modInitContainers
	return mod
}

// mergeEnvs merges the existing envs with the new envs. Existing envs are
// replaced by the new envs.
func MergeEnvs(existingEnvs []corev1.EnvVar, newEnvs ...corev1.EnvVar) ([]corev1.EnvVar, bool) {
	return mergeEnvs(false, nil, existingEnvs, newEnvs...)
}

func addOptionalEnvsToContainer(keepAllExisting bool, keepExistingEnvVars map[string]bool, c *corev1.Container, inEnvs ...corev1.EnvVar) bool {
	envs, ok := mergeEnvs(keepAllExisting, keepExistingEnvVars, c.Env, inEnvs...)
	c.Env = envs
	return ok
}

// mergeEnvs merges the existing envs with the new envs. Existing envs are
// replaced by the new envs unless keepExisting is true or the env is in keepExistingEnvVars.
func mergeEnvs(keepAllExisting bool, keepExistingEnvVars map[string]bool, existingEnvs []corev1.EnvVar, newEnvs ...corev1.EnvVar) ([]corev1.EnvVar, bool) {
	if keepExistingEnvVars == nil {
		keepExistingEnvVars = make(map[string]bool)
	}
	envs := make([]corev1.EnvVar, len(newEnvs))
	copy(envs, newEnvs)
	mod := false
	for i, existingEnv := range existingEnvs {
		for j, env := range envs {
			if existingEnv.Name == env.Name {
				if keepAllExisting || keepExistingEnvVars[env.Name] {
					envs = slices.Delete(envs, j, j+1)
					break
				}
				if env.ValueFrom != nil {
					mod = mod || existingEnv.Value != "" || existingEnv.ValueFrom == nil
					existingEnvs[i].Value = ""
					existingEnvs[i].ValueFrom = env.ValueFrom
				} else if env.Value != "" {
					mod = mod || existingEnv.Value != env.Value || existingEnv.ValueFrom != nil
					existingEnvs[i].Value = env.Value
					existingEnvs[i].ValueFrom = nil
				}
				envs = slices.Delete(envs, j, j+1)
				break
			}
		}
		if len(envs) == 0 {
			return existingEnvs, mod
		}
	}
	mod = mod || len(envs) != 0
	existingEnvs = append(existingEnvs, envs...)
	return existingEnvs, mod
}
