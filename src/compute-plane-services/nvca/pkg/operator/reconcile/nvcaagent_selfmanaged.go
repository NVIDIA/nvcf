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
	"fmt"
	"path"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

const (
	// IdentitySourcePSAT projects a Kubernetes ServiceAccount token at
	// /var/run/secrets/tokens/token with audience nvcf-icms:{clusterID}. Default
	// for self-hosted clusters.
	IdentitySourcePSAT = "psat"
	// IdentitySourceSPIRE provisions a SPIFFE CSI volume + spiffe-token-fetcher
	// sidecar that writes a JWT-SVID to /var/run/secrets/tokens/token.
	IdentitySourceSPIRE = "spire"

	// NVCAClusterRegistrationConfigMapName is the name of the ConfigMap that stores
	// clusterId and clusterGroupId for self-managed clusters.
	NVCAClusterRegistrationConfigMapName = "nvca-cluster-registration"
)

func IsSelfHosted(nb *nvidiaiov1.NVCFBackend) bool {
	return nb.Spec.ClusterSource == nvcaoptypes.ClusterSourceSelfHosted
}

// detectIdentitySource resolves the identity source for self-hosted clusters.
// Today only "" (treated as default) and "psat" are accepted. Anything else
// returns an error so a misconfigured chart fails loud rather than silently
// routing to the wrong code path.
//
// SPIRE scaffolding (applySPIREIdentity, IdentitySourceSPIRE, the
// spiffe-token-fetcher sidecar) remains in-tree for future re-introduction
// but is intentionally not selectable in this release: SPIRE is not yet
// validated end-to-end against ICMS / NATS auth-callout, so exposing it as an
// option would be misleading.
//
// Vault-agent is no longer a valid identity source for self-hosted clusters —
// it only ever made sense for helm-managed/ngc-managed clusters, which take
// a different reconciler path entirely (see IsSelfHosted callers).
func detectIdentitySource(mode string) (string, error) {
	switch mode {
	case "", IdentitySourcePSAT:
		return IdentitySourcePSAT, nil
	case IdentitySourceSPIRE:
		return "", fmt.Errorf(
			"identity-source %q is not yet supported end-to-end; valid values: %q (empty defaults to %q)",
			mode, IdentitySourcePSAT, IdentitySourcePSAT)
	}
	return "", fmt.Errorf("invalid identity-source %q for self-hosted cluster; valid values: %q (empty defaults to %q)",
		mode, IdentitySourcePSAT, IdentitySourcePSAT)
}

func applySelfManagedNVCADeployment(ctx context.Context, nb *nvidiaiov1.NVCFBackend, nvcaDeployment *appsv1.Deployment,
	_ kubernetes.Interface, identitySourceMode string, nvcaImage string,
) error {
	log := core.GetLogger(ctx)
	log.Infof("Applying self-managed NVCA deployment configuration for backend %s/%s cluster %s",
		nb.Namespace, nb.Name, nb.Spec.ClusterConfig.ClusterName)

	identitySource, err := detectIdentitySource(identitySourceMode)
	if err != nil {
		return fmt.Errorf("self-managed NVCA deployment for backend %s/%s: %w", nb.Namespace, nb.Name, err)
	}
	log.Infof("Identity source resolved to: %s", identitySource)

	// All containers need the default security context.
	containers := nvcaDeployment.Spec.Template.Spec.Containers
	for i := range containers {
		sc := getDefaultContainerSecurityContext()
		sc.RunAsUser = ptr.To(int64(1000))
		containers[i].SecurityContext = sc
	}

	clusterID := nb.Spec.ClusterConfig.ClusterID

	// Apply identity-specific volumes and mounts first so the common
	// registration volume below sits at the end of the list — simpler for
	// readers to reason about and what TestSetupNVCADeployment_SelfHosted
	// expects.
	switch identitySource {
	case IdentitySourcePSAT:
		applyPSATIdentity(ctx, nvcaDeployment, clusterID)
	case IdentitySourceSPIRE:
		applySPIREIdentity(ctx, nvcaDeployment, nvcaImage, clusterID)
	}

	// Registration volume is common across all identity modes.
	nvcaDeployment.Spec.Template.Spec.Volumes = append(nvcaDeployment.Spec.Template.Spec.Volumes,
		corev1.Volume{
			Name: "nvca-self-managed-registration",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	)

	// Mount the registration volume into the agent container for all modes.
	for i := range containers {
		if containers[i].Name == "agent" {
			containers[i].VolumeMounts = append(containers[i].VolumeMounts,
				corev1.VolumeMount{
					Name:      "nvca-self-managed-registration",
					MountPath: "/var/run/secrets/nvca-self-managed-registration",
					ReadOnly:  true,
				},
			)
		}
	}

	log.Infof("Successfully applied self-managed NVCA deployment configuration for %s", nb.Spec.ClusterConfig.ClusterName)
	return nil
}

const (
	clusterIssuedTokenDir      = "/var/run/secrets/tokens"
	clusterIssuedTokenFilePath = clusterIssuedTokenDir + "/token"
)

// applyPSATIdentity adds the projected ServiceAccount token volume for PSAT identity.
// The agent container reads the token from /var/run/secrets/tokens/token; audience
// is "nvcf-icms:{clusterID}" so ICMS can resolve the cluster by audience at verify time.
func applyPSATIdentity(ctx context.Context, nvcaDeployment *appsv1.Deployment, clusterID string) {
	core.GetLogger(ctx).Debugf("Adding projected SA token volume for PSAT identity")

	nvcaDeployment.Spec.Template.Spec.Volumes = append(nvcaDeployment.Spec.Template.Spec.Volumes,
		corev1.Volume{
			Name: "nvca-token",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{
							ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
								Audience:          fmt.Sprintf("nvcf-icms:%s", clusterID),
								ExpirationSeconds: ptr.To(int64(3600)),
								Path:              path.Base(clusterIssuedTokenFilePath),
							},
						},
					},
				},
			},
		},
	)

	containers := nvcaDeployment.Spec.Template.Spec.Containers
	for i := range containers {
		if containers[i].Name == "agent" {
			containers[i].VolumeMounts = append(containers[i].VolumeMounts,
				corev1.VolumeMount{
					Name:      "nvca-token",
					MountPath: clusterIssuedTokenDir,
					ReadOnly:  true,
				},
			)
		}
	}
}

// applySPIREIdentity adds a SPIRE CSI volume, a shared emptyDir for the token,
// and a spiffe-token-fetcher native sidecar (init container with restartPolicy Always).
func applySPIREIdentity(ctx context.Context, nvcaDeployment *appsv1.Deployment, nvcaImage string, clusterID string) {
	log := core.GetLogger(ctx)
	log.Infof("Configuring SPIRE identity: CSI volume + spiffe-token-fetcher sidecar")

	nvcaDeployment.Spec.Template.Spec.Volumes = append(nvcaDeployment.Spec.Template.Spec.Volumes,
		corev1.Volume{
			Name: "spire-agent-socket",
			VolumeSource: corev1.VolumeSource{
				CSI: &corev1.CSIVolumeSource{
					Driver:   "csi.spiffe.io",
					ReadOnly: ptr.To(true),
				},
			},
		},
		corev1.Volume{
			Name: "nvca-token",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	)

	containers := nvcaDeployment.Spec.Template.Spec.Containers
	for i := range containers {
		if containers[i].Name == "agent" {
			containers[i].VolumeMounts = append(containers[i].VolumeMounts,
				corev1.VolumeMount{
					Name:      "nvca-token",
					MountPath: clusterIssuedTokenDir,
					ReadOnly:  true,
				},
			)
		}
	}

	restartAlways := corev1.ContainerRestartPolicyAlways
	sc := getDefaultContainerSecurityContext()
	sc.RunAsUser = ptr.To(int64(1000))

	nvcaDeployment.Spec.Template.Spec.InitContainers = append(nvcaDeployment.Spec.Template.Spec.InitContainers,
		corev1.Container{
			Name:          "spiffe-token-fetcher",
			Image:         nvcaImage,
			Command:       []string{"/usr/bin/spiffe-token-fetcher"},
			RestartPolicy: &restartAlways,
			Args: []string{
				"--socket=unix:///run/spire/spire-agent.sock",
				fmt.Sprintf("--audience=nvcf-icms:%s", clusterID),
				"--output=" + clusterIssuedTokenFilePath,
				"--health-port=8081",
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "spire-agent-socket", MountPath: "/run/spire", ReadOnly: true},
				{Name: "nvca-token", MountPath: clusterIssuedTokenDir},
			},
			SecurityContext: sc,
		},
	)
}
