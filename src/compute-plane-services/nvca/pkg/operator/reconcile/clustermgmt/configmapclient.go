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

package clustermgmt

import (
	"context"
	"fmt"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

// ConfigMapClient implements Client and reads a clusterDTO stored in a local ConfigMap.
// Behaviour can be customised by providing additional clusterMapper functions.
// This replaces the original HelmManagedClient and SelfManagedClient which were
// thin variations of the same logic.
//
// NOTE: Keep the field names exported so that existing alias-based wrappers
// (e.g. SelfManagedClient) can still be initialised via struct literals in tests.
// We purposely do not export the struct itself to avoid leaking further API
// surface outside this package.
//
// IMPORTANT: Do not embed logics here that could be achieved with mappers – keep
// this generic.

type ConfigMapClient struct {
	configMapFetcher   func(ctx context.Context) (*corev1.ConfigMap, error)
	envType            nvidiaiov1.EnvType
	nvcfBackendMappers []clusterMapper
}

// newConfigMapClient is an internal helper that constructs a ConfigMapClient
// with the default mappers plus optional ones provided by the caller.
func newConfigMapClient(
	envType nvidiaiov1.EnvType,
	fetcher func(ctx context.Context) (*corev1.ConfigMap, error),
	vaultMountPathTemplate string,
	extraMappers ...clusterMapper,
) *ConfigMapClient {
	base := []clusterMapper{
		withRootNVCFBackendMapper(),
		withVaultConfigNVCFBackendMapper(vaultMountPathTemplate),
		withGPUDiscoveryNVCFBackendMapper(),
		withNVCFImageCredentialUpdaterMapper(),
		withWebhookConfigMapper(),
		withSharedStorageImageMapper(),
		withMiniServiceMapper(),
		withOTelCollectorMapper(),
		withAgentConfigMapper(),
	}
	base = append(base, extraMappers...)

	return &ConfigMapClient{
		configMapFetcher:   fetcher,
		envType:            envType,
		nvcfBackendMappers: base,
	}
}

// GetCluster satisfies the Client interface.
func (c *ConfigMapClient) GetCluster(ctx context.Context, _ string) (*Cluster, error) {
	log := core.GetLogger(ctx)

	// Fetch ConfigMap
	configMap, err := c.configMapFetcher(ctx)
	if err != nil {
		log.WithError(err).Errorf("failed to read configmap file")
		return nil, fmt.Errorf("failed to read configmap file: %w", err)
	}

	raw, ok := configMap.Data["cluster-dto.yaml"]
	if !ok || strings.TrimSpace(raw) == "" {
		log.Errorf("cluster-dto.yaml not found in configmap %s", configMap.Name)
		return nil, fmt.Errorf("cluster-dto.yaml not found in configmap %s", configMap.Name)
	}

	var dto clusterDTO
	if err := yaml.Unmarshal([]byte(raw), &dto); err != nil {
		log.WithError(err).Errorf("failed to convert CR to cluster")
		return nil, fmt.Errorf("failed to convert CR to NVCFBackend: %w", err)
	}

	dest := &Cluster{NVCFBackend: &nvidiaiov1.NVCFBackend{}}
	for _, mapper := range c.nvcfBackendMappers {
		if err := mapper(ctx, c.envType, &dto, dest); err != nil {
			return nil, err
		}
	}

	return dest, nil
}
