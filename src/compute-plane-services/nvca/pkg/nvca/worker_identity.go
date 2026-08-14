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
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	// workerSAPrefix is the ServiceAccount name prefix for worker pods.
	workerSAPrefix = "nvcf-worker-"
	// workerTokenVolumeName is the projected SAT volume name injected into worker pods.
	workerTokenVolumeName = "nvcf-worker-token"
	// workerTokenMountPath is where the projected SAT is mounted.
	workerTokenMountPath = "/var/run/secrets/tokens"
	// WorkerTokenFilePathEnvKey is the env var telling the worker where to find its token.
	WorkerTokenFilePathEnvKey = "NVCF_TOKEN_FILE_PATH"
	// WorkerIdentitySourceEnvKey tells the worker which identity mechanism is active.
	WorkerIdentitySourceEnvKey = "NVCF_IDENTITY_SOURCE"
	// WorkerIdentitySourcePSAT is the value written to NVCF_IDENTITY_SOURCE for PSAT tokens.
	WorkerIdentitySourcePSAT = "psat"
)

// workerTokenExpirationSeconds is the requested SAT lifetime (must be addressable).
var workerTokenExpirationSeconds int64 = 900

// workerSAName returns the ServiceAccount name for a given pod (instance ID).
func workerSAName(podName string) string {
	return workerSAPrefix + podName
}

// workerTokenFilePath is the full path to the projected token file.
const workerTokenFilePath = workerTokenMountPath + "/token"

// ensureWorkerServiceAccount creates the worker ServiceAccount for a pod if it does not already
// exist. Returns the SA (newly created or pre-existing) so the caller can read its UID.
func ensureWorkerServiceAccount(
	ctx context.Context,
	clients *kubeclients.KubeClients,
	namespace, podName string,
) (*corev1.ServiceAccount, error) {
	name := workerSAName(podName)
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	created, err := clients.K8s.CoreV1().ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{})
	if err == nil {
		return created, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("create worker ServiceAccount %s/%s: %w", namespace, name, err)
	}
	existing, err := clients.K8s.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get worker ServiceAccount %s/%s: %w", namespace, name, err)
	}
	return existing, nil
}

// injectWorkerIdentity adds the projected SAT volume and worker identity env vars to a pod spec.
// The pod's ServiceAccountName is also set to the worker SA so the token is minted for that SA.
func injectWorkerIdentity(pod *corev1.Pod, clusterID, podName string) {
	saName := workerSAName(podName)
	audience := "nvcf-icms:" + clusterID

	pod.Spec.ServiceAccountName = saName
	pod.Spec.AutomountServiceAccountToken = boolPtr(false)

	volume := corev1.Volume{
		Name: workerTokenVolumeName,
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{
					{
						ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
							Audience:          audience,
							ExpirationSeconds: &workerTokenExpirationSeconds,
							Path:              "token",
						},
					},
				},
			},
		},
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, volume)

	mount := corev1.VolumeMount{
		Name:      workerTokenVolumeName,
		MountPath: workerTokenMountPath,
		ReadOnly:  true,
	}
	envVars := []corev1.EnvVar{
		{Name: WorkerTokenFilePathEnvKey, Value: workerTokenFilePath},
		{Name: WorkerIdentitySourceEnvKey, Value: WorkerIdentitySourcePSAT},
	}

	// Inject into all non-init containers so every worker process can read the token.
	for i := range pod.Spec.Containers {
		pod.Spec.Containers[i].VolumeMounts = append(pod.Spec.Containers[i].VolumeMounts, mount)
		pod.Spec.Containers[i].Env = append(pod.Spec.Containers[i].Env, envVars...)
	}
}

// buildWorkerAuth constructs the WorkerAuth payload for an active pod.
// Returns nil if the pod does not use a worker SA (i.e., not in worker-identity mode).
func buildWorkerAuth(
	ctx context.Context,
	clients *kubeclients.KubeClients,
	namespace string,
	pod *corev1.Pod,
) *types.WorkerAuth {
	log := core.GetLogger(ctx)
	saName := pod.Spec.ServiceAccountName
	if saName == "" || len(saName) <= len(workerSAPrefix) || saName[:len(workerSAPrefix)] != workerSAPrefix {
		return nil
	}
	sa, err := clients.K8s.CoreV1().ServiceAccounts(namespace).Get(ctx, saName, metav1.GetOptions{})
	if err != nil {
		log.WithError(err).WithField("serviceAccount", saName).Debug("Failed to fetch worker SA for WorkerAuth")
		return nil
	}
	sub := fmt.Sprintf("system:serviceaccount:%s:%s", namespace, saName)
	return &types.WorkerAuth{
		Sub:   sub,
		SAuid: string(sa.UID),
		WorkerIdentifiers: []types.WorkerIdentifier{
			{Name: pod.Name, UID: string(pod.UID)},
		},
	}
}

func boolPtr(b bool) *bool { return &b }
