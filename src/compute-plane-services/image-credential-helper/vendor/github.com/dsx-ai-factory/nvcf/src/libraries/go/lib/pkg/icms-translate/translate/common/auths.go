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
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/go-containerregistry/pkg/name"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// Image pull secret envs with 3rd party registry support
	ContainerRegistriesCredentialsEnv = "CONTAINER_REGISTRIES_CREDENTIALS"
	SidecarRegistryCredentialEnv      = "SIDECAR_REGISTRY_CREDENTIAL"
	HelmRegistriesCredentialsEnv      = "HELM_REGISTRIES_CREDENTIALS" //nolint:gosec // false positive: env var name, not credential

	// Legacy image pull secret envs
	FunctionContainerCredentialEnvLegacy = "INFERENCE_CONTAINER_CREDENTIAL" //nolint:gosec // false positive: env var name, not credential
	TaskContainerCredentialEnvLegacy     = "TASK_CONTAINER_CREDENTIAL"      //nolint:gosec // false positive: env var name, not credential
	WorkerCredentialEnvLegacy            = "SIDECAR_CREDENTIAL"             //nolint:gosec // false positive: env var name, not credential
)

// FilterImageRegistryAuths filters out all image registry servers from secrets in regAuthCfg
// that do not match the "host[:port]" of any image in images.
func FilterImageRegistryAuths(regAuthCfg RegistryAuthConfig, images ...string) (secrets []RegistryAuthSecret, err error) {
	var errs []error
	imageRegSrvs := map[string]struct{}{}
	for _, image := range images {
		if image == "" {
			continue
		}
		regStr, err := parseImageToRegistry(image)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		imageRegSrvs[regStr] = struct{}{}
	}

	if len(errs) != 0 {
		return nil, errors.Join(errs...)
	}

	if len(imageRegSrvs) == 0 {
		return regAuthCfg.K8sSecrets, nil
	}

	return collectMatchingRegistryAuths(regAuthCfg, imageRegSrvs), nil
}

// FilterHelmRegistryAuths filters out all Helm registry servers from secrets in regAuthCfg
// that do not match the "host[:port]" of helmChartURL.
func FilterHelmRegistryAuths(regAuthCfg RegistryAuthConfig, helmChartURL string) (secrets []RegistryAuthSecret, err error) {
	u, err := url.Parse(helmChartURL)
	if err != nil {
		return nil, fmt.Errorf("parse helm chart URL: %v", err)
	}

	registry := u.Hostname()
	if port := u.Port(); port != "" {
		registry = fmt.Sprintf("%s:%s", registry, port)
	}

	return collectMatchingRegistryAuths(regAuthCfg, map[string]struct{}{registry: {}}), nil
}

func parseImageToRegistry(image string) (string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", err
	}
	regStr := ref.Context().RegistryStr()
	// Revert alias override in
	// https://github.com/google/go-containerregistry/blob/59a4b85/pkg/name/registry.go#L134-L138
	const defaultRegistryAlias = "docker.io"
	if regStr == name.DefaultRegistry {
		return defaultRegistryAlias, nil
	}
	return regStr, nil
}

func collectMatchingRegistryAuths(regAuthCfg RegistryAuthConfig, imageRegSrvs map[string]struct{}) (secrets []RegistryAuthSecret) {
	// A user can have two separate accounts with a single registry
	// and have their containers in two different accounts,
	// so all auths for a particular registry must be collected.
	for _, secret := range regAuthCfg.K8sSecrets {
		newAuths := map[string]RegistryAuth{}
		for srv, auth := range secret.Auths {
			if _, ok := imageRegSrvs[srv]; ok {
				newAuths[srv] = auth
			}
		}
		if len(newAuths) != 0 {
			secrets = append(secrets, RegistryAuthSecret{Auths: newAuths})
		}
	}

	return secrets
}

func ParseWorkloadImagePullSecrets(nameBase string, allEnvSet map[string]string, isHelm bool) (pullSecrets []*corev1.Secret, err error) {
	// If isHelm is true, FilterImageRegistryAuths will return all secrets
	// since there is no way to determine which images are present in the helm chart
	// at this point. The caller must do this if filtering is desired.
	var image string
	if !isHelm {
		for _, env := range WorkloadImageEnvs {
			if img := allEnvSet[env]; img != "" {
				image = img
				break
			}
		}
		if image == "" {
			return nil, fmt.Errorf("no workload container image found")
		}
	}

	if cred := allEnvSet[ContainerRegistriesCredentialsEnv]; cred != "" {
		regAuthCfg, err := decodeRegistryAuthConfig(cred)
		if err != nil {
			return nil, fmt.Errorf("decode image registry auth config: %v", err)
		}
		if len(regAuthCfg.K8sSecrets) == 0 {
			return nil, nil
		}
		filteredSecrets, err := FilterImageRegistryAuths(regAuthCfg, image)
		if err != nil {
			return nil, fmt.Errorf("filter image registry pull secrets: %v", err)
		}
		for i, secret := range filteredSecrets {
			name := fmtImagePullSecretName(fmt.Sprintf("workload-%s-regcred-%d", nameBase, i))
			pullSecret, err := makeImagePullSecret(name, secret)
			if err != nil {
				return nil, fmt.Errorf("make image registry pull secret: %v", err)
			}
			pullSecrets = append(pullSecrets, pullSecret)
		}
	}

	return pullSecrets, nil
}

func DecodeWorkloadImageRegistryAuthConfig(allEnvSet map[string]string) (regAuthConfig RegistryAuthConfig, found bool, err error) {
	cred := allEnvSet[ContainerRegistriesCredentialsEnv]
	if cred == "" {
		return RegistryAuthConfig{}, false, nil
	}
	regAuthConfig, err = decodeRegistryAuthConfig(cred)
	if err != nil {
		return RegistryAuthConfig{}, false, fmt.Errorf("decode image registry auth config: %v", err)
	}

	return regAuthConfig, len(regAuthConfig.K8sSecrets) > 0, nil
}

func ParseWorkerImagePullSecrets(nameBase string, allEnvSet map[string]string) (pullSecrets []*corev1.Secret, err error) {
	var images []string
	for _, env := range WorkerImageEnvs {
		if image := allEnvSet[env]; image != "" {
			images = append(images, image)
		}
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("no worker container envs found")
	}

	if cred := allEnvSet[SidecarRegistryCredentialEnv]; cred != "" {
		regAuthSecret, err := decodeRegistryAuthSecret(cred)
		if err != nil {
			return nil, fmt.Errorf("decode worker image registry auth secret: %v", err)
		}
		if len(regAuthSecret.Auths) == 0 {
			return nil, nil
		}
		regAuthCfg := RegistryAuthConfig{K8sSecrets: []RegistryAuthSecret{regAuthSecret}}
		filteredSecrets, err := FilterImageRegistryAuths(regAuthCfg, images...)
		if err != nil {
			return nil, fmt.Errorf("filter worker image registry pull secrets: %v", err)
		}
		for i, secret := range filteredSecrets {
			name := fmtImagePullSecretName(fmt.Sprintf("worker-%s-regcred-%d", nameBase, i))
			pullSecret, err := makeImagePullSecret(name, secret)
			if err != nil {
				return nil, fmt.Errorf("make worker image registry pull secret: %v", err)
			}
			pullSecrets = append(pullSecrets, pullSecret)
		}
	}

	return pullSecrets, nil
}

func ParseHelmChartDownloadSecret(helmChartURL string, allEnvSet map[string]string) (*corev1.Secret, bool, error) {
	hcDownloadSecret := &corev1.Secret{}
	hcDownloadSecret.Name = HelmChartDownloadSecretName
	hcDownloadSecret.Type = corev1.SecretTypeOpaque

	authCfg, found, err := ParseHelmWorkloadAuthConfig(helmChartURL, allEnvSet)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	if len(authCfg.K8sSecrets) == 0 {
		return nil, false, nil
	}

	// For now, only one secret per registry is allowed.
	secret := authCfg.K8sSecrets[0]
	for _, auth := range secret.Auths {
		decodedBytes, err := base64.StdEncoding.DecodeString(auth.Auth)
		if err != nil {
			return nil, false, fmt.Errorf("decode helm registry secret: %v", err)
		}
		split := bytes.SplitN(decodedBytes, []byte{':'}, 2)
		if len(split) != 2 {
			return nil, false, fmt.Errorf("helm registry secret  does not contain ':'")
		}
		hcDownloadSecret.StringData = map[string]string{
			"username": string(bytes.TrimSpace(split[0])),
			"password": string(bytes.TrimSpace(split[1])),
		}
		break
	}

	return hcDownloadSecret, true, nil
}

func ParseHelmWorkloadAuthConfig(helmChartURL string, allEnvSet map[string]string) (authCfg HelmAuthConfig, found bool, err error) {
	if helmChartURL == "" {
		return HelmAuthConfig{}, false, fmt.Errorf("helm chart URL is empty")
	}
	// Parse 3rd party and legacy Helm chart download secret(s).
	cred := allEnvSet[HelmRegistriesCredentialsEnv]
	if cred == "" {
		return HelmAuthConfig{}, false, nil
	}

	regAuthCfg, err := decodeRegistryAuthConfig(cred)
	if err != nil {
		return HelmAuthConfig{}, false, fmt.Errorf("decode helm registry auth config: %v", err)
	}
	if len(regAuthCfg.K8sSecrets) == 0 {
		return HelmAuthConfig{}, false, nil
	}
	if authCfg.K8sSecrets, err = FilterHelmRegistryAuths(regAuthCfg, helmChartURL); err != nil {
		return HelmAuthConfig{}, false, fmt.Errorf("filter helm registry pull secrets: %v", err)
	}

	return authCfg, len(authCfg.K8sSecrets) > 0, nil
}

func decodeRegistryAuthConfig(cred string) (cfg RegistryAuthConfig, err error) {
	credBytes, err := base64.StdEncoding.DecodeString(cred)
	if err != nil {
		return RegistryAuthConfig{}, err
	}
	if err := json.Unmarshal(credBytes, &cfg); err != nil {
		return RegistryAuthConfig{}, err
	}
	return cfg, nil
}

func decodeRegistryAuthSecret(cred string) (secret RegistryAuthSecret, err error) {
	credBytes, err := base64.StdEncoding.DecodeString(cred)
	if err != nil {
		return RegistryAuthSecret{}, err
	}
	if err := json.Unmarshal(credBytes, &secret); err != nil {
		return RegistryAuthSecret{}, err
	}
	return secret, nil
}

func makeImagePullSecret(name string, raSecret RegistryAuthSecret) (*corev1.Secret, error) {
	if len(raSecret.Auths) == 0 {
		return nil, fmt.Errorf("pull secret auths are empty")
	}
	b, err := json.Marshal(raSecret)
	if err != nil {
		return nil, fmt.Errorf("encode pull secret auths: %v", err)
	}
	secret := &corev1.Secret{}
	secret.Name = name
	secret.Type = corev1.SecretTypeDockerConfigJson
	secret.StringData = map[string]string{corev1.DockerConfigJsonKey: string(b)}
	return secret, nil
}

func fmtImagePullSecretName(name string) string {
	if len(name) <= validation.DNS1123SubdomainMaxLength {
		return name
	}
	return name[:validation.DNS1123SubdomainMaxLength]
}
