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

package mscontroller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/miniservice"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	// miniserviceWorkerTokenVolumeName is the projected SAT volume name injected into the utils pod.
	miniserviceWorkerTokenVolumeName = "nvcf-worker-token"
	// miniserviceWorkerTokenMountPath is where the projected SAT is mounted inside the container.
	miniserviceWorkerTokenMountPath = "/var/run/secrets/tokens"
	// miniserviceWorkerTokenFilePath is the full path to the projected token file.
	miniserviceWorkerTokenFilePath = miniserviceWorkerTokenMountPath + "/token"
	// miniserviceWorkerTokenFilePathEnvKey is the env var pointing at the token file.
	miniserviceWorkerTokenFilePathEnvKey = "NVCF_TOKEN_FILE_PATH"
	// miniserviceWorkerIdentitySourceEnvKey indicates the active identity mechanism.
	miniserviceWorkerIdentitySourceEnvKey = "NVCF_IDENTITY_SOURCE"
	// miniserviceWorkerIdentitySourcePSAT is the value written when PSAT is the mechanism.
	miniserviceWorkerIdentitySourcePSAT = "psat"
)

// miniserviceWorkerTokenExpirationSeconds is the requested SAT lifetime.
var miniserviceWorkerTokenExpirationSeconds int64 = 900

// setWorkerIdentityMeta marks an identity object as NVCA infrastructure belonging to the MiniService.
func setWorkerIdentityMeta(meta *metav1.ObjectMeta, msName string) {
	if meta.Labels == nil {
		meta.Labels = map[string]string{}
	}
	if meta.Annotations == nil {
		meta.Annotations = map[string]string{}
	}
	meta.Labels[nvcatypes.MiniserviceNameLabel] = msName
	meta.Annotations[nvcatypes.InfraObjectAnnotationKey] = "true"
}

// ensureWorkerIdentity creates the worker ServiceAccount and reconciles the worker Role and
// RoleBinding in namespace. The Role is always reset to grant nothing and the RoleBinding to its
// single worker subject, so a pre-existing object of the same name cannot grant the worker SA
// permissions.
func ensureWorkerIdentity(ctx context.Context, c client.Client, namespace, msName string) error {
	name := miniservice.WorkerServiceAccountName

	automount := false
	sa := &corev1.ServiceAccount{
		ObjectMeta:                   metav1.ObjectMeta{Name: name, Namespace: namespace},
		AutomountServiceAccountToken: &automount,
	}
	setWorkerIdentityMeta(&sa.ObjectMeta, msName)
	if err := c.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create worker ServiceAccount %s/%s: %w", namespace, name, err)
	}

	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, role, func() error {
		setWorkerIdentityMeta(&role.ObjectMeta, msName)
		role.Rules = nil
		return nil
	}); err != nil {
		return fmt.Errorf("reconcile worker Role %s/%s: %w", namespace, name, err)
	}

	rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, rb, func() error {
		setWorkerIdentityMeta(&rb.ObjectMeta, msName)
		rb.RoleRef = rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     name,
		}
		rb.Subjects = []rbacv1.Subject{
			{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: namespace},
		}
		return nil
	}); err != nil {
		return fmt.Errorf("reconcile worker RoleBinding %s/%s: %w", namespace, name, err)
	}

	return nil
}

// injectWorkerTokenVolume assigns the worker ServiceAccount to pod, disables the default token
// automount so only the audience-bound token is present, adds the projected SAT volume, and
// injects worker identity env vars into all non-init containers.
// The token audience is "nvcf-icms:<clusterID>" with a 900-second expiry.
// The volume is mounted read-only at /var/run/secrets/tokens in all non-init containers.
func injectWorkerTokenVolume(pod *corev1.Pod, clusterID string) {
	pod.Spec.ServiceAccountName = miniservice.WorkerServiceAccountName
	automount := false
	pod.Spec.AutomountServiceAccountToken = &automount
	audience := "nvcf-icms:" + clusterID

	volume := corev1.Volume{
		Name: miniserviceWorkerTokenVolumeName,
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{
					{
						ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
							Audience:          audience,
							ExpirationSeconds: &miniserviceWorkerTokenExpirationSeconds,
							Path:              "token",
						},
					},
				},
			},
		},
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, volume)

	mount := corev1.VolumeMount{
		Name:      miniserviceWorkerTokenVolumeName,
		MountPath: miniserviceWorkerTokenMountPath,
		ReadOnly:  true,
	}
	envVars := []corev1.EnvVar{
		{Name: miniserviceWorkerTokenFilePathEnvKey, Value: miniserviceWorkerTokenFilePath},
		{Name: miniserviceWorkerIdentitySourceEnvKey, Value: miniserviceWorkerIdentitySourcePSAT},
	}

	for i := range pod.Spec.Containers {
		pod.Spec.Containers[i].VolumeMounts = append(pod.Spec.Containers[i].VolumeMounts, mount)
		pod.Spec.Containers[i].Env = append(pod.Spec.Containers[i].Env, envVars...)
	}
}
