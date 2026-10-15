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
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	cmnsecret "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/secret"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcaoperatorerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/errors"
)

const (
	NVCFBackendForceRolloutAnnotation = "nvca.nvcf.nvidia.io/forcedRolloutAt"
)

func hasNGCServiceAPIKeyChangedCheck(ctx context.Context,
	currentNGCServiceAPIKeyFetcher cmnsecret.TokenFetcher,
	secretGetter func(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Secret, error),
) func() bool {
	return func() bool {
		log := core.GetLogger(ctx)
		curAPIKey, err := currentNGCServiceAPIKeyFetcher.FetchToken(ctx)
		if err != nil {
			log.WithError(err).Error("failed to fetch current NGC Service API key")
			return false
		}

		// Check the API key secret and compare it to the original value
		ngcServiceAPIKeySecret, err := secretGetter(ctx, NGCServiceAPIKeySecretName, metav1.GetOptions{})
		if err != nil {
			log.WithError(err).Error("failed to fetch NGC Service API key secret")
			return false
		}

		// Read the API key from the secret and compare it to the original value
		ngcServiceAPIKey, ok := ngcServiceAPIKeySecret.Data[NGCServiceAPIKeySecretName]
		if !ok {
			log.Debugf("%s key secret does not contain the expected data key", NGCServiceAPIKeySecretName)
		}

		// Compare the original API key with the secret's API key
		if curAPIKey != string(ngcServiceAPIKey) {
			log.Infof("NGC Service API key changed from secret %s", NGCServiceAPIKeySecretName)
			return true
		}

		return false
	}
}

func hasImagePullSecretChangedCheck(ctx context.Context,
	currentNGCServiceAPIKeyFetcher cmnsecret.TokenFetcher,
	secretGetter func(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Secret, error),
	repoServer string,
) func() bool {
	return func() bool {
		log := core.GetLogger(ctx)
		// Get the current NGC API Key
		curAPIKey, err := currentNGCServiceAPIKeyFetcher.FetchToken(ctx)
		if err != nil {
			log.WithError(err).Error("failed to fetch current NGC Service API key")
			return false
		}

		// Get the image pull secret from the NGC API Key
		curImagePullSecretData, err := getImagePullSecretDockerConfigJSONFromNGCKey(repoServer, curAPIKey)
		if err != nil {
			log.WithError(err).Error("failed to create image pull secret from NGC API Key for comparison")
			return false
		}

		// Get the image pull secret from nvca-system namespace
		imagePullSecret, err := secretGetter(ctx, NVCAImagePullSecretName, metav1.GetOptions{})
		if err != nil {
			log.WithError(err).Error("failed to get image pull secret from K8s secret")
			return false
		}

		// Compare the current image pull secret to the K8s secret
		if !bytes.Equal(curImagePullSecretData, imagePullSecret.Data[".dockerconfigjson"]) {
			log.Infof("Image pull secret changed from secret %s", NVCAImagePullSecretName)
			return true
		}

		return false
	}
}

func hasNVCFBackendRolloutAnnotationChanged(ctx context.Context, oldNBAnnotations, newNBAnnotations map[string]string) bool {
	log := core.GetLogger(ctx)
	// If the last updated time is not set, return true
	if newNBAnnotations[NVCFBackendForceRolloutAnnotation] == "" {
		log.Debugf("NVCFBackend %s has no force rollout annotation %s, skipping sync",
			newNBAnnotations[NVCFBackendForceRolloutAnnotation],
			NVCFBackendForceRolloutAnnotation)
		return false
	}

	return oldNBAnnotations[NVCFBackendForceRolloutAnnotation] != newNBAnnotations[NVCFBackendForceRolloutAnnotation]
}

func hasNVCFBackendChangedCheck(ctx context.Context, nb *nvidiaiov1.NVCFBackend) func() bool {
	return func() bool {
		log := core.GetLogger(ctx)

		diff := cmp.Diff(nb.Spec.NVCFBackendSpecT, nb.Status.NVCFBackendSpecT, cmpopts.EquateEmpty(),
			cmpopts.IgnoreTypes(nvidiaiov1.AgentConfig{}))
		if diff != "" {
			log.Infof("NVCFBackend %s has changed", nb.Name)
			log.WithField("diff", diff).Debugf("NVCFBackend %s diff", nb.Name)
			return true
		}

		log.Tracef("NVCFBackend %s is unchanged", nb.Name)
		return false
	}
}

func hasAgentWorkerConfigOptionsChanged(ctx context.Context, newNVCAConfig nvidiaiov1.NVCFWorkerConfig,
	nbStatus nvidiaiov1.NVCFBackendStatus) func() bool {
	srcConfig := nbStatus.AgentConfig.NVCFWorkerConfig //nolint:staticcheck
	return func() bool {
		log := core.GetLogger(ctx)
		if newNVCAConfig.CacheMountOptionsEnabled != srcConfig.CacheMountOptionsEnabled ||
			newNVCAConfig.CacheMountOptions != srcConfig.CacheMountOptions ||
			newNVCAConfig.WorkerDegradationPeriod != srcConfig.WorkerDegradationPeriod {
			log.Infof("Agent worker config changed from %+v to %+v", nbStatus.AgentConfig.NVCFWorkerConfig, newNVCAConfig) //nolint:staticcheck
			return true
		}
		return false
	}
}

func hasAgentDeploymentConfigChanged(ctx context.Context, newDeployConfig nvidiaiov1.DeploymentConfig,
	nbStatus nvidiaiov1.NVCFBackendStatus) func() bool {
	srcConfig := nbStatus.AgentConfig.DeploymentConfig //nolint:staticcheck
	return func() bool {
		log := core.GetLogger(ctx)
		if newDeployConfig.PriorityClassName != srcConfig.PriorityClassName ||
			newDeployConfig.NodeSelectorKey != srcConfig.NodeSelectorKey ||
			newDeployConfig.NodeSelectorValue != srcConfig.NodeSelectorValue ||
			newDeployConfig.SecretMirrorLabelSelector != srcConfig.SecretMirrorLabelSelector ||
			newDeployConfig.SecretMirrorSourceNamespace != srcConfig.SecretMirrorSourceNamespace ||
			newDeployConfig.GenerateImagePullSecret != srcConfig.GenerateImagePullSecret {
			log.Infof("Agent deployment config changed from %+v to %+v", srcConfig, newDeployConfig)
			return true
		}
		return false
	}
}

func hasAdditionalImagePullSecretsChanged(ctx context.Context, newSecrets []corev1.LocalObjectReference,
	nbStatus nvidiaiov1.NVCFBackendStatus) func() bool {
	return func() bool {
		log := core.GetLogger(ctx)

		// Get previous values from status
		previousSecrets := nbStatus.AdditionalImagePullSecrets

		// Always return true to ensure secrets are synced (handles additions, removals, and changes)
		if !cmp.Equal(newSecrets, previousSecrets, cmpopts.EquateEmpty()) {
			log.Infof("Additional image pull secrets changed from %v to %v", previousSecrets, newSecrets)
			return true
		}

		return false
	}
}

func hasNetworkConfigChangedCheck(ctx context.Context, newDDCSIPAllowList, newK8sClusterNetworkCIDRs []string,
	nbStatus nvidiaiov1.NVCFBackendStatus) func() bool {
	return func() bool {
		log := core.GetLogger(ctx)

		// Get previous values from NVCFBackend status (survives operator restarts)
		previousDDCSIPAllowList := nbStatus.DDCSIPAllowList
		previousK8sClusterNetworkCIDRs := nbStatus.K8sClusterNetworkCIDRs

		// Check if DDCSIPAllowList has changed
		if !cmp.Equal(newDDCSIPAllowList, previousDDCSIPAllowList, cmpopts.EquateEmpty()) {
			log.Infof("DDCS IP Allow List changed from %v to %v", previousDDCSIPAllowList, newDDCSIPAllowList)
			return true
		}

		// Check if K8sClusterNetworkCIDRs has changed
		if !cmp.Equal(newK8sClusterNetworkCIDRs, previousK8sClusterNetworkCIDRs, cmpopts.EquateEmpty()) {
			log.Infof("K8s Cluster Network CIDRs changed from %v to %v", previousK8sClusterNetworkCIDRs, newK8sClusterNetworkCIDRs)
			return true
		}

		return false
	}
}

func hasEnvOverridesChangedCheck(ctx context.Context,
	newFunctionEnvOverridesB64, newTaskEnvOverridesB64 string,
	nbStatus nvidiaiov1.NVCFBackendStatus,
) func() bool {
	return func() bool {
		log := core.GetLogger(ctx)

		if newFunctionEnvOverridesB64 != nbStatus.FunctionEnvOverridesB64 {
			// Log change detection without exposing potentially sensitive payload contents
			log.Infof("Function env overrides changed (old_len=%d, new_len=%d)",
				len(nbStatus.FunctionEnvOverridesB64), len(newFunctionEnvOverridesB64))
			return true
		}

		if newTaskEnvOverridesB64 != nbStatus.TaskEnvOverridesB64 {
			// Log change detection without exposing potentially sensitive payload contents
			log.Infof("Task env overrides changed (old_len=%d, new_len=%d)",
				len(nbStatus.TaskEnvOverridesB64), len(newTaskEnvOverridesB64))
			return true
		}

		return false
	}
}

func hasNVCAResourcesChangedCheck(ctx context.Context,
	newAgentResources, newWebhookResources, newOTelCollectorResources corev1.ResourceRequirements,
	nbStatus nvidiaiov1.NVCFBackendStatus,
) func() bool {
	return func() bool {
		log := core.GetLogger(ctx)

		if nbStatus.AgentConfig.AgentResources == nil { //nolint:staticcheck
			if !objectsEqual(newAgentResources, corev1.ResourceRequirements{}) {
				log.Infof("Agent resources changed from nil to %v", newAgentResources)
				return true
			}
		} else if !objectsEqual(newAgentResources, *nbStatus.AgentConfig.AgentResources) { //nolint:staticcheck
			log.Infof("Agent resources changed from %v to %v", nbStatus.AgentConfig.AgentResources, newAgentResources) //nolint:staticcheck
			return true
		}

		if nbStatus.AgentConfig.WebhookResources == nil { //nolint:staticcheck
			if !objectsEqual(newWebhookResources, corev1.ResourceRequirements{}) {
				log.Infof("Webhook resources changed from nil to %v", newWebhookResources)
				return true
			}
		} else if !objectsEqual(newWebhookResources, *nbStatus.AgentConfig.WebhookResources) { //nolint:staticcheck
			log.Infof("Webhook resources changed from %v to %v", nbStatus.AgentConfig.WebhookResources, newWebhookResources) //nolint:staticcheck
			return true
		}

		if nbStatus.AgentConfig.OTelCollectorResources == nil { //nolint:staticcheck
			if !objectsEqual(newOTelCollectorResources, corev1.ResourceRequirements{}) {
				log.Infof("OTel collector resources changed from nil to %v", newOTelCollectorResources)
				return true
			}
		} else if !objectsEqual(newOTelCollectorResources, *nbStatus.AgentConfig.OTelCollectorResources) { //nolint:staticcheck
			log.Infof("OTel collector resources changed from %v to %v",
				nbStatus.AgentConfig.OTelCollectorResources, newOTelCollectorResources) //nolint:staticcheck
			return true
		}

		return false
	}
}

func hasOTelCollectorConfigChangedCheck(ctx context.Context,
	newCfg *nvidiaiov1.OTelCollectorConfig,
	nbStatus nvidiaiov1.NVCFBackendStatus,
) func() bool {
	return func() bool {
		log := core.GetLogger(ctx)
		prev := nbStatus.OTelCollectorConfig
		if prev == nil {
			log.Infof("OTel collector config not yet stored in status (enabled=%v, repo=%q, tag=%q), triggering rollout",
				newCfg.Enabled, newCfg.ImageConfig.Repository, newCfg.ImageConfig.Tag)
			return true
		}
		if newCfg.Enabled != prev.Enabled {
			log.Infof("OTel collector enabled changed from %v to %v", prev.Enabled, newCfg.Enabled)
			return true
		}
		if newCfg.ImageConfig.Repository != prev.ImageConfig.Repository {
			log.Infof("OTel collector image repo changed from %q to %q", prev.ImageConfig.Repository, newCfg.ImageConfig.Repository)
			return true
		}
		if newCfg.ImageConfig.Tag != prev.ImageConfig.Tag {
			log.Infof("OTel collector image tag changed from %q to %q", prev.ImageConfig.Tag, newCfg.ImageConfig.Tag)
			return true
		}
		return false
	}
}

func (bc *BackendK8sCache) newAgentConfigChangedCheck(ctx context.Context, nb *nvidiaiov1.NVCFBackend) (func() bool, error) {
	genCfg, err := bc.newAgentConfig(ctx, nb)
	if err != nil {
		return nil, nvcaoperatorerrors.FatalError(err)
	}
	mergeCfg, _, err := bc.getAgentConfigToMerge(ctx)
	if err != nil {
		return nil, fmt.Errorf("get agent config to merge: %w", err)
	}
	existingCfgCM, err := bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	switch {
	case k8serrors.IsNotFound(err),
		(err == nil && (existingCfgCM.Data == nil || strings.TrimSpace(existingCfgCM.Data[agentConfigFile]) == "")):
		return func() bool { return true }, nil
	case err != nil:
		return nil, err
	}

	genCfgData, err := encodeAgentConfig(genCfg, mergeCfg, nb.Spec.AgentConfig.NATSURL, agentHostOverrideConfig(nb, bc.envType))
	if err != nil {
		return nil, fmt.Errorf("encode and merge generated config: %w", err)
	}

	existingCfgData := existingCfgCM.Data[agentConfigFile]

	return func() bool {
		return !bytes.Equal(bytes.TrimSpace(genCfgData), bytes.TrimSpace([]byte(existingCfgData)))
	}, nil
}
