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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	cmnhttp "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/http"
	cmnsecret "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/secret"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

// Ensure NGCManagedClient implements Client
var _ Client = &NGCManagedClient{}

const (
	DefaultRootNGCAPIURL                = "https://api.ngc.nvidia.com"
	DefaultGXCacheNamespace             = "gxcache"
	DefaultCrowdstrikeInjectorNamespace = "crowdstrike-injector"
	ShaderCacheLabelKey                 = "nvca.nvcf.nvidia.io/gxcache-client-inject"
	DefaultNVCFBackendSyncPeriod        = 15 * time.Minute
	// DefaultVaultOAuthClientMountPathTemplate is the default template for constructing
	// the Vault OAuth client mount path. Use %s as placeholder for clientID.
	//
	// Deprecated: This default will be removed in a future release.
	DefaultVaultOAuthClientMountPathTemplate = "nvidia/services/oauth/clients/%s/kv/secret"
)

// NGCManagedClient acts as the Cluster Management API Client
type NGCManagedClient struct {
	httpClient                        *http.Client
	rootNGCAPIURL                     string
	tokenFetcher                      cmnsecret.TokenFetcher
	envType                           nvidiaiov1.EnvType
	vaultOAuthClientMountPathTemplate string

	// NVCF mapper resources
	nvcfBackendMappers []clusterMapper
}

type NGCManagedClientOption func(*NGCManagedClient)

func WithRootNGCAPIURL(rootNGCAPIURL string) NGCManagedClientOption {
	return func(c *NGCManagedClient) {
		if rootNGCAPIURL != "" {
			c.rootNGCAPIURL = strings.TrimSuffix(rootNGCAPIURL, "/")
		}
	}
}

func WithVaultOAuthClientMountPathTemplate(template string) NGCManagedClientOption {
	return func(c *NGCManagedClient) {
		if template != "" {
			c.vaultOAuthClientMountPathTemplate = template
		}
	}
}

func NewNGCManagedClient(ctx context.Context,
	tokenFetcher cmnsecret.TokenFetcher,
	appName string,
	envType nvidiaiov1.EnvType,
	options ...NGCManagedClientOption,
) (*NGCManagedClient, error) {
	log := core.GetLogger(ctx)

	client := &NGCManagedClient{
		rootNGCAPIURL:                     DefaultRootNGCAPIURL,
		vaultOAuthClientMountPathTemplate: DefaultVaultOAuthClientMountPathTemplate,
		httpClient: cmnhttp.NewRetryableClient(ctx,
			cmnhttp.WithAppVersionUserAgent(appName)),
		tokenFetcher: tokenFetcher,
		envType:      envType,
	}
	// Set the httpclient transport to the default one wrapped with otelhttp
	// so we can trace requests
	client.httpClient.Transport = otelhttp.NewTransport(client.httpClient.Transport)

	// Apply the options before setting up mappers
	for _, opt := range options {
		opt(client)
	}

	// Set up mappers after options are applied so they can use configured values
	client.nvcfBackendMappers = []clusterMapper{
		withRootNVCFBackendMapper(),
		withVaultConfigNVCFBackendMapper(client.vaultOAuthClientMountPathTemplate),
		withGPUDiscoveryNVCFBackendMapper(),
		withNVCFImageCredentialUpdaterMapper(),
		withWebhookConfigMapper(),
		withSharedStorageImageMapper(),
		withOTelCollectorMapper(),
		withAgentConfigMapper(),
		withClusterSourceMapper(nvcaoptypes.ClusterSourceNGCManaged),
	}

	// Verify we can parse the default URL
	_, err := url.Parse(client.rootNGCAPIURL)
	if err != nil {
		log.WithError(err).Errorf("failed to parse the root NGC API URL %s", client.rootNGCAPIURL)
		return nil, err
	}

	return client, nil
}

func (c *NGCManagedClient) GetCluster(ctx context.Context, clusterID string) (*Cluster, error) {
	log := core.GetLogger(ctx)

	srcCluster, err := c.getCluster(ctx, clusterID)
	if err != nil {
		log.WithError(err).Errorf("failed to retrieve the cluster clusterID=%s failed", clusterID)
		return nil, err
	}

	destCluster := &Cluster{
		NVCFBackend: &nvidiaiov1.NVCFBackend{},
	}
	for _, mapper := range c.nvcfBackendMappers {
		err := mapper(ctx, c.envType, srcCluster, destCluster)
		if err != nil {
			return nil, err
		}
	}

	return destCluster, nil
}

func (c *NGCManagedClient) getCluster(ctx context.Context, clusterID string) (*clusterDTO, error) {
	log := core.GetLogger(ctx)
	// Create the cluster retrieval URL using the legacy /v2/icms/clusters endpoint.
	clusterReqURL, err := url.JoinPath(c.rootNGCAPIURL, "/v2/icms/clusters", clusterID)
	if err != nil {
		log.WithError(err).Errorf("failed to generate cluster retrieval URL with root %s", c.rootNGCAPIURL)
		return nil, err
	}

	// Fetch the token for authentication
	token, err := c.tokenFetcher.FetchToken(ctx)
	if err != nil {
		log.WithError(err).Error("failed to retrieve the token for getCluster call")
		return nil, err
	}

	// Request the provided cluster
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clusterReqURL, nil)
	if err != nil {
		log.WithError(err).Errorf("failed to create the request %s %s", http.MethodGet, clusterReqURL)
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.WithError(err).Errorf("request %s %s failed for ClusterID=%s", req.Method, req.URL, clusterID)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			err = errors.NewNotFound(nvidiaiov1.Resource(clusterID), clusterID)
		} else {
			err = fmt.Errorf("request %s %s failed for ClusterID=%s, status=%s, statusCode=%d",
				req.Method, req.URL, clusterID, resp.Status, resp.StatusCode)
		}
		log.WithError(err).Error("ICMS get cluster request failed")
		return nil, err
	}

	clusterDTO := clusterDTO{}
	err = json.NewDecoder(resp.Body).Decode(&clusterDTO)
	if err != nil {
		log.WithError(err).Error("failed to decode the response")
		return nil, err
	}

	return &clusterDTO, nil
}

type clusterMapper func(ctx context.Context, envType nvidiaiov1.EnvType, src *clusterDTO, dest *Cluster) error

func removeString(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return
}

func withRootNVCFBackendMapper() clusterMapper {
	return func(ctx context.Context, envType nvidiaiov1.EnvType, src *clusterDTO, dest *Cluster) error {
		log := core.GetLogger(ctx)

		dest.NVCFBackend.Name = src.Name
		dest.NVCFBackend.Spec.Version = src.NVCAVersion

		// AccountConfig
		dest.NVCFBackend.Spec.AccountConfig = nvidiaiov1.AccountConfig{
			NCAID: src.NCAID,
		}

		// ClusterConfig
		k8sNetworkCIDRs := src.K8sClusterNetworkCIDRs
		// Append ClusterNetworkCIDRAllowedRange from ClusterConfigurations if present
		if len(src.ClusterConfigurations) > 0 {
			if cidrRange := src.ClusterConfigurations[ClusterNetworkCIDRAllowedRangeConfig]; cidrRange != "" {
				// Parse the cidrRange which may be comma-separated
				// Handle formats like: "1.2.3.4/32,5.6.7.8/32" or '"1.2.3.4/32","5.6.7.8/32"'
				cidrs := strings.Split(cidrRange, ",")
				for _, cidr := range cidrs {
					// Trim whitespace and quotes from each CIDR
					cleanCIDR := strings.Trim(strings.TrimSpace(cidr), `"'`)
					if cleanCIDR != "" {
						k8sNetworkCIDRs = append(k8sNetworkCIDRs, cleanCIDR)
					}
				}
			}
		}

		dest.NVCFBackend.Spec.ClusterConfig = nvidiaiov1.ClusterConfig{
			ClusterID:              src.ID,
			ClusterName:            src.Name,
			Description:            src.Description,
			ClusterGroupName:       src.GroupName,
			ClusterGroupID:         src.GroupID,
			Region:                 src.Region,
			CloudProvider:          src.CloudProvider,
			K8sClusterNetworkCIDRs: k8sNetworkCIDRs,
		}
		// Map the attributes to NVCFBackend attributes
		// The format is ${attributeKey}=${attributeValue}
		// If not attribute value is provided in the source attribute
		// we automatically add "true" as a default.
		// CustomAttributes will override Attributes on key collision.
		attributes, err := normalizeAttributes(src.Attributes, src.CustomAttributes)
		if err != nil {
			return err
		}
		dest.NVCFBackend.Spec.ClusterConfig.Attributes = attributes

		// ICMSConfig
		dest.NVCFBackend.Spec.ICMSConfig = nvidiaiov1.ICMSConfig{
			ICMSServiceURL: src.ICMSConfig.ICMSServiceURL,
			TokenURL:       src.ICMSConfig.TokenURL,
		}

		clientID := src.getClientID()

		// featureGate
		dest.NVCFBackend.Spec.FeatureGate = nvidiaiov1.FeatureGate{
			Values: removeString(src.Capabilities, string(DynamicGPUDiscovery)),
		}
		// Normalize the feature gate values
		for i, v := range dest.NVCFBackend.Spec.FeatureGate.Values {
			unquoted, err := strconv.Unquote(v)
			if err != nil {
				log.WithError(err).Tracef("failed to unquote the feature gate value %s, defaulting to %s", v, v)
				unquoted = v
			}
			dest.NVCFBackend.Spec.FeatureGate.Values[i] = strings.TrimSpace(unquoted)
		}

		// ---- Apply dynamic defaults previously handled by Helm ----
		const (
			icmsURLProd  = "https://icms.nvcf.nvidia.com"
			icmsURLStage = "https://stg.icms.nvcf.nvidia.com"
		)

		// ICMSServiceURL
		if dest.NVCFBackend.Spec.ICMSConfig.ICMSServiceURL == "" {
			if envType == nvidiaiov1.EnvTypeStage {
				dest.NVCFBackend.Spec.ICMSConfig.ICMSServiceURL = icmsURLStage
			} else {
				dest.NVCFBackend.Spec.ICMSConfig.ICMSServiceURL = icmsURLProd
			}
		}

		// Set OAuthConfig from cluster configuration.
		if clientID != "" {
			dest.NVCFBackend.Spec.OAuthConfig = nvidiaiov1.OAuthConfig{
				ClientID:             clientID,
				PublicKeysetEndpoint: src.ICMSConfig.PublicKeysetEndpoint,
				TokenURL:             dest.NVCFBackend.Spec.ICMSConfig.TokenURL, // Use ICMSConfig after defaults applied
			}
		}

		return nil
	}
}

func withVaultConfigNVCFBackendMapper(vaultMountPathTemplate string) clusterMapper {
	return func(_ context.Context, envType nvidiaiov1.EnvType, src *clusterDTO, dest *Cluster) error {
		dest.NVCFBackend.Spec.VaultConfig = nvidiaiov1.VaultConfig{}
		if src.vaultEnabled() {
			dest.NVCFBackend.Spec.VaultConfig.Enabled = true
			dest.NVCFBackend.Spec.VaultConfig.Address = src.VaultConfig.Address
			dest.NVCFBackend.Spec.VaultConfig.OAuthConfigRole = fmt.Sprintf("k8s_%s_bart_jwt_role", src.Name)
			dest.NVCFBackend.Spec.VaultConfig.AuthMountPath = fmt.Sprintf("auth/jwt/k8s/%s", src.Name)
			// Use pre-computed path from DTO if available (Helm-managed), otherwise compute from template
			vaultMountPath := src.OAuthClientMountPath
			if vaultMountPath == "" {
				clientID := src.getClientID()
				vaultMountPath = fmt.Sprintf(vaultMountPathTemplate, clientID)
			}
			dest.NVCFBackend.Spec.VaultConfig.OAuthClientMountPath = vaultMountPath

			// ---- Apply default Vault address if missing ----
			const (
				vaultAddrStage = "https://stg.vault.nvidia.com:443"
				vaultAddrProd  = "https://:443"
			)
			if dest.NVCFBackend.Spec.VaultConfig.Address == "" {
				if envType == nvidiaiov1.EnvTypeStage {
					dest.NVCFBackend.Spec.VaultConfig.Address = vaultAddrStage
				} else {
					dest.NVCFBackend.Spec.VaultConfig.Address = vaultAddrProd
				}
			}
		}
		return nil
	}
}

func withGPUDiscoveryNVCFBackendMapper() clusterMapper {
	return func(ctx context.Context, _ nvidiaiov1.EnvType, src *clusterDTO, dest *Cluster) error {
		// If DynamicGPUDiscovery is disabled use static
		if !src.containsCapability(DynamicGPUDiscovery) {
			// If DynamicGPUDiscovery is disabled, use static GPU allocation
			// Check for gpusB64 first
			if src.GPUsB64 != "" {
				gpusCfgRaw, err := base64.StdEncoding.DecodeString(src.GPUsB64)
				if err != nil {
					core.GetLogger(ctx).WithError(err).Errorf("failed to decode the gpusB64 field from the config for cluster %s", src.Name)
					return err
				}
				gpuCfg := []registrationGPU{}
				err = json.Unmarshal(gpusCfgRaw, &gpuCfg)
				if err != nil {
					core.GetLogger(ctx).WithError(err).Errorf("failed to unmarshal the gpusB64 field from the config for cluster %s", src.Name)
					return err
				}
				src.GPUs = gpuCfg
				dest.NVCFBackend.Spec.ClusterConfig.GPUDiscovery = nvidiaiov1.GPUDiscoveryConfig{
					Static: &nvidiaiov1.StaticGPUDiscoveryConfig{
						GPUConfig: string(gpusCfgRaw),
					},
				}
			} else {
				buff := bytes.Buffer{}
				err := json.NewEncoder(&buff).Encode(src.GPUs)
				if err != nil {
					core.GetLogger(ctx).WithError(err).Errorf("failed to encode the gpus field from the response for cluster %s", src.Name)
					return err
				}
				dest.NVCFBackend.Spec.ClusterConfig.GPUDiscovery = nvidiaiov1.GPUDiscoveryConfig{
					Static: &nvidiaiov1.StaticGPUDiscoveryConfig{
						GPUConfig: buff.String(),
					},
				}
			}
			// Calculate the total allocated GPU capacity
			var totalAllocatedGPUCapacity uint64
			for _, v := range src.GPUs {
				totalAllocatedGPUCapacity += uint64(v.Capacity)
			}
			dest.NVCFBackend.Spec.ClusterConfig.GPUDiscovery.Static.AllocatedGPUCapacity = totalAllocatedGPUCapacity
		} else {
			// Use dynamic GPU allocation
			dest.NVCFBackend.Spec.ClusterConfig.GPUDiscovery = nvidiaiov1.GPUDiscoveryConfig{
				Dynamic: &nvidiaiov1.DynamicGPUDiscoveryConfig{},
			}
		}

		return nil
	}
}

func withNVCFImageCredentialUpdaterMapper() clusterMapper {
	return func(_ context.Context, _ nvidiaiov1.EnvType, src *clusterDTO, dest *Cluster) error {
		var cfg *nvidiaiov1.ImageCredHelperConfig
		if src.ImageCredHelper != nil {
			cfg = &nvidiaiov1.ImageCredHelperConfig{}
			// This image is configured from the legacy config source or Helm values so defaults are not needed.
			cfg.ImageConfig.Repository = src.ImageCredHelper.ImageConfig.Repository
			cfg.ImageConfig.Tag = src.ImageCredHelper.ImageConfig.Tag
		}
		dest.NVCFBackend.Spec.ImageCredHelper = cfg
		return nil
	}
}

// withWebhookConfigMapper propagates WebhookConfig from the cluster DTO
// (helm values / ConfigMap) into the NVCFBackend Spec. Without this, the
// operator's webhook image resolver falls back to a derived "nvca/nvca:" tag
// (empty after the colon, InvalidImageName at pod admission time).
func withWebhookConfigMapper() clusterMapper {
	return func(_ context.Context, _ nvidiaiov1.EnvType, src *clusterDTO, dest *Cluster) error {
		if src.WebhookConfig == nil {
			return nil
		}
		dest.NVCFBackend.Spec.WebhookConfig.ImageConfig.Repository = src.WebhookConfig.ImageConfig.Repository
		dest.NVCFBackend.Spec.WebhookConfig.ImageConfig.Tag = src.WebhookConfig.ImageConfig.Tag
		return nil
	}
}

func withSharedStorageImageMapper() clusterMapper {
	return func(_ context.Context, _ nvidiaiov1.EnvType, src *clusterDTO, dest *Cluster) error {
		if src.SharedStorage != nil &&
			src.SharedStorage.ImageRepository != "" &&
			src.SharedStorage.ImageTag != "" {
			dest.NVCFBackend.Spec.SharedStorageImage = &nvidiaiov1.ImageConfig{
				Repository: src.SharedStorage.ImageRepository,
				Tag:        src.SharedStorage.ImageTag,
			}
		}
		return nil
	}
}

func withMiniServiceMapper() clusterMapper {
	return func(_ context.Context, _ nvidiaiov1.EnvType, src *clusterDTO, dest *Cluster) error {
		if src.MiniService != nil && src.MiniService.HelmReValServiceURL != "" {
			if dest.NVCFBackend.Spec.ClusterConfig.MiniService == nil {
				dest.NVCFBackend.Spec.ClusterConfig.MiniService = &nvidiaiov1.MiniServiceConfig{}
			}
			dest.NVCFBackend.Spec.ClusterConfig.MiniService.HelmReValServiceURL = src.MiniService.HelmReValServiceURL
		}
		return nil
	}
}

func withOTelCollectorMapper() clusterMapper {
	return func(_ context.Context, _ nvidiaiov1.EnvType, src *clusterDTO, dest *Cluster) error {
		var cfg *nvidiaiov1.OTelCollectorConfig
		if src.OTelCollector != nil {
			cfg = &nvidiaiov1.OTelCollectorConfig{}
			cfg.Enabled = src.OTelCollector.Enabled
			cfg.ImageConfig.Repository = src.OTelCollector.ImageConfig.Repository
			cfg.ImageConfig.Tag = src.OTelCollector.ImageConfig.Tag
		}
		dest.NVCFBackend.Spec.OTelCollector = cfg
		return nil
	}
}

func withAgentConfigMapper() clusterMapper {
	return func(_ context.Context, _ nvidiaiov1.EnvType, src *clusterDTO, dest *Cluster) error {
		if src.ClusterConfigurations == nil {
			src.ClusterConfigurations = make(CaseInsensitiveClusterConfigMap)
		}

		// Map DeploymentConfig from ClusterConfigurations
		dest.NVCFBackend.Spec.AgentConfig.DeploymentConfig = nvidiaiov1.DeploymentConfig{
			PriorityClassName:           src.ClusterConfigurations[AgentPriorityClassNameConfig],
			NodeSelectorKey:             src.ClusterConfigurations[AgentNodeSelectorLabelKeyConfig],
			NodeSelectorValue:           src.ClusterConfigurations[AgentNodeSelectorLabelValueConfig],
			SecretMirrorSourceNamespace: src.ClusterConfigurations[NVCASecretMirrorSourceNamespaceConfig],
			SecretMirrorLabelSelector:   src.ClusterConfigurations[NVCASecretMirrorLabelSelectorConfig],
		}
		if src.Agent != nil {
			dest.NVCFBackend.Spec.AgentConfig.HelmReValStageOAuthTokenURL = src.Agent.HelmReValStageOAuthTokenURL
			dest.NVCFBackend.Spec.AgentConfig.HelmReValStageOAuthPublicKeysetEndpoint = src.Agent.HelmReValStageOAuthPublicKeysetEndpoint
			dest.NVCFBackend.Spec.AgentConfig.HelmReValProdOAuthTokenURL = src.Agent.HelmReValProdOAuthTokenURL
			dest.NVCFBackend.Spec.AgentConfig.HelmReValProdOAuthPublicKeysetEndpoint = src.Agent.HelmReValProdOAuthPublicKeysetEndpoint
			dest.NVCFBackend.Spec.AgentConfig.RolloverServiceStageOAuthTokenURL = src.Agent.RolloverServiceStageOAuthTokenURL
			dest.NVCFBackend.Spec.AgentConfig.RolloverServiceStageOAuthPublicKeysetEndpoint = src.Agent.RolloverServiceStageOAuthPublicKeysetEndpoint
			dest.NVCFBackend.Spec.AgentConfig.RolloverServiceProdOAuthTokenURL = src.Agent.RolloverServiceProdOAuthTokenURL
			dest.NVCFBackend.Spec.AgentConfig.RolloverServiceProdOAuthPublicKeysetEndpoint = src.Agent.RolloverServiceProdOAuthPublicKeysetEndpoint
			dest.NVCFBackend.Spec.AgentConfig.FunctionDeploymentStagesStageOAuthTokenURL = src.Agent.FunctionDeploymentStagesStageOAuthTokenURL
			dest.NVCFBackend.Spec.AgentConfig.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint =
				src.Agent.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint
			dest.NVCFBackend.Spec.AgentConfig.FunctionDeploymentStagesProdOAuthTokenURL = src.Agent.FunctionDeploymentStagesProdOAuthTokenURL
			dest.NVCFBackend.Spec.AgentConfig.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint =
				src.Agent.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint
			dest.NVCFBackend.Spec.AgentConfig.Tolerations = append(
				[]corev1.Toleration(nil),
				src.Agent.Tolerations...,
			)
			if src.Agent.NATSURL != "" {
				dest.NVCFBackend.Spec.AgentConfig.NATSURL = ptr.To(src.Agent.NATSURL)
			}
		}

		// Parse CacheMountOptionsEnabled from string.
		// Default to true for upgrade compatibility when the key is missing.
		cacheMountOptionsEnabled := true
		cacheMountOptions := src.ClusterConfigurations[ModelCacheVolumeMountOptionsConfig]
		if enabledStr, exists := src.ClusterConfigurations[ModelCacheVolumeMountOptionEnabledConfig]; exists {
			// Key exists - accept "enabled" as enabled, anything else as disabled.
			cacheMountOptionsEnabled = strings.EqualFold(enabledStr, "enabled")
		} else if cacheMountOptions == "" {
			// Key doesn't exist and no options provided - default to enabled with standard options for upgrade compatibility.
			cacheMountOptions = nvcaoptypes.DefaultCacheCSIMountOptions
		}

		// Parse WorkerDegradationPeriod from minutes to time.Duration
		var workerDegradationPeriod time.Duration
		if minutesStr := src.ClusterConfigurations[NVCFWorkerDegradationPeriodMinutesConfig]; minutesStr != "" {
			minutes, err := strconv.Atoi(minutesStr)
			if err != nil {
				return fmt.Errorf("failed to parse worker degradation period minutes: %w", err)
			}
			workerDegradationPeriod = time.Duration(minutes) * time.Minute
		}

		// Map NVCFWorkerConfig
		dest.NVCFBackend.Spec.AgentConfig.NVCFWorkerConfig = nvidiaiov1.NVCFWorkerConfig{
			CacheMountOptionsEnabled: cacheMountOptionsEnabled,
			CacheMountOptions:        cacheMountOptions,
			WorkerDegradationPeriod:  workerDegradationPeriod,
		}

		return nil
	}
}

func withClusterSourceMapper(clusterSource nvcaoptypes.ClusterSource) clusterMapper {
	return func(_ context.Context, _ nvidiaiov1.EnvType, _ *clusterDTO, dest *Cluster) error {
		dest.NVCFBackend.Spec.ClusterSource = clusterSource
		return nil
	}
}

// withBootstrapRegistrationMapper creates a mapper that reads cluster registration IDs
// from a separate ConfigMap populated by the bootstrap job. This allows the cluster IDs
// to persist across helm upgrades.
func withBootstrapRegistrationMapper(fetcher func(ctx context.Context) (*corev1.ConfigMap, error)) clusterMapper {
	return func(ctx context.Context, _ nvidiaiov1.EnvType, src *clusterDTO, dest *Cluster) error {
		cm, err := fetcher(ctx)
		if err != nil {
			if errors.IsNotFound(err) {
				return nil // OK on fresh install before bootstrap runs
			}
			return err
		}

		// Override cluster IDs if present in the bootstrap registration ConfigMap
		if id := cm.Data["clusterId"]; id != "" {
			src.ID = id
			dest.NVCFBackend.Spec.ClusterConfig.ClusterID = id
		}
		if groupID := cm.Data["clusterGroupId"]; groupID != "" {
			src.GroupID = groupID
			dest.NVCFBackend.Spec.ClusterConfig.ClusterGroupID = groupID
		}
		return nil
	}
}

func normalizeAttributes(attributes, customAttributes []string) ([]string, error) {
	// Helper to find duplicates in a slice
	findDuplicates := func(attrs []string) []string {
		counts := make(map[string]int)
		var keysInOrder []string
		for _, attr := range attrs {
			key, _, _ := strings.Cut(strings.TrimSpace(strings.Trim(strings.TrimSpace(attr), `"`)), "=")
			key = strings.TrimSpace(key)
			if key != "" {
				if _, exists := counts[key]; !exists {
					keysInOrder = append(keysInOrder, key)
				}
				counts[key]++
			}
		}
		var duplicates []string
		for _, key := range keysInOrder {
			if counts[key] > 1 {
				duplicates = append(duplicates, key)
			}
		}
		return duplicates
	}

	// Check for duplicates within base attributes
	if dupes := findDuplicates(attributes); len(dupes) > 0 {
		return nil, fmt.Errorf("duplicate attributes found: %+q in %+q", dupes, attributes)
	}

	// Check for duplicates within custom attributes
	if dupes := findDuplicates(customAttributes); len(dupes) > 0 {
		return nil, fmt.Errorf("duplicate attributes found: %+q in %+q", dupes, customAttributes)
	}

	attrMap := make(map[string]string)
	process := func(attrs []string) error {
		for _, v := range attrs {
			v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"`))
			attrKey, attrVal, found := strings.Cut(v, "=")
			attrKey = strings.TrimSpace(attrKey)

			if attrKey == "" {
				return fmt.Errorf("attribute key cannot be empty in %q", v)
			}

			if !found {
				attrVal = "true"
			} else {
				attrVal = strings.TrimSpace(strings.Trim(strings.TrimSpace(attrVal), `"`))
			}
			attrMap[attrKey] = attrVal
		}
		return nil
	}

	// Process base then custom, so custom will override.
	if err := process(attributes); err != nil {
		return nil, err
	}
	if err := process(customAttributes); err != nil {
		return nil, err
	}

	if len(attrMap) == 0 {
		return []string{}, nil
	}

	finalAttributes := make([]string, 0, len(attrMap))
	for k, v := range attrMap {
		finalAttributes = append(finalAttributes, fmt.Sprintf("%s=%s", k, v))
	}

	sort.Strings(finalAttributes)
	return finalAttributes, nil
}
