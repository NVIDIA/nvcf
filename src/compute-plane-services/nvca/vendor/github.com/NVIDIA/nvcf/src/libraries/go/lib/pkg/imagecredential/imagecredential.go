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

package imagecredential

import (
	"fmt"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	// ImageCredsSecretNameSuffix is appended to the workload id component for image credential secrets.
	ImageCredsSecretNameSuffix = "-image-creds"
)

// NewUpdaterCronJob returns a CronJob that refreshes image pull secrets with short-lived registry tokens.
func NewUpdaterCronJob(name, imageRef, namespaceLabelSelectorStr string) *batchv1.CronJob {
	historyLimit := int32(1)
	cj := &batchv1.CronJob{}
	cj.Name = name
	cj.Spec = batchv1.CronJobSpec{
		// Run every hour.
		Schedule:                   "0 * * * *",
		ConcurrencyPolicy:          batchv1.ForbidConcurrent,
		SuccessfulJobsHistoryLimit: &historyLimit,
		FailedJobsHistoryLimit:     &historyLimit,
		JobTemplate: batchv1.JobTemplateSpec{
			Spec: newJobSpec(imageRef, "", "", namespaceLabelSelectorStr),
		},
	}

	return cj
}

// NewInitJob returns a Job that refreshes image pull secrets before workload pods need them.
func NewInitJob(name, imageRef, targetNamespace, secretLabelSelectorStr string) *batchv1.Job {
	job := &batchv1.Job{}
	job.Name = name
	job.Spec = newJobSpec(imageRef, targetNamespace, secretLabelSelectorStr, "")

	return job
}

func newJobSpec(imageRef, targetNamespace, secretLabelSelectorStr, namespaceLabelSelectorStr string) batchv1.JobSpec {
	var pullPolicy corev1.PullPolicy
	if strings.HasSuffix(imageRef, ":latest") {
		pullPolicy = corev1.PullAlways
	} else {
		pullPolicy = corev1.PullIfNotPresent
	}

	args := []string{
		"-global",
	}

	if targetNamespace != "" {
		args = append(args, "-target-namespace", targetNamespace)
	}
	if secretLabelSelectorStr != "" {
		args = append(args, "-secret-label-selector", secretLabelSelectorStr)
	}
	if namespaceLabelSelectorStr != "" {
		args = append(args, "-namespace-label-selector", namespaceLabelSelectorStr)
	}

	trueVal := true
	completions := int32(1)
	backoffLimit := int32(3)
	jobSpec := batchv1.JobSpec{
		Completions:  &completions,
		BackoffLimit: &backoffLimit,
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				RestartPolicy:                corev1.RestartPolicyOnFailure,
				AutomountServiceAccountToken: &trueVal,
				ServiceAccountName:           "default",
				Containers: []corev1.Container{{
					Name:            "updater",
					Image:           imageRef,
					ImagePullPolicy: pullPolicy,
					Command:         []string{"/usr/bin/image-credential-helper"},
					Args:            args,
				}},
			},
		},
	}

	return jobSpec
}

// NewImageCredsSecret returns an opaque Secret containing the registry credential env payloads.
func NewImageCredsSecret(idComponent string, allEnvSet map[string]string) (*corev1.Secret, error) {
	if idComponent == "" {
		return nil, fmt.Errorf("id component is required")
	}
	envs := []string{
		common.ContainerRegistriesCredentialsEnv,
		common.SidecarRegistryCredentialEnv,
	}
	data := make(map[string][]byte, len(envs))
	for _, env := range envs {
		value, ok := allEnvSet[env]
		if !ok {
			return nil, fmt.Errorf("env %s not found", env)
		}
		data[env] = []byte(value)
	}

	secret := &corev1.Secret{}
	secret.Name = idComponent + ImageCredsSecretNameSuffix
	secret.Type = corev1.SecretTypeOpaque
	secret.Data = data

	return secret, nil
}
