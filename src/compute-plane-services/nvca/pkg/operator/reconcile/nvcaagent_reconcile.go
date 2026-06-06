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
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/sirupsen/logrus"
	yamlv3 "gopkg.in/yaml.v3"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvcaoperatorerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/reconcile/clustermgmt"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	ClusterName                       = "nvcf.nvidia.io/cluster-name"
	ClusterGroupKey                   = "nvcf.nvidia.io/cluster-group"
	InstanceLabelKey                  = "app.kubernetes.io/instance"
	ManagedbyLabelKey                 = "app.kubernetes.io/managed-by"
	NameLabelKey                      = "app.kubernetes.io/name"
	AdditionalImagePullSecretLabelKey = "nvca.nvcf.nvidia.io/additional-image-pull-secret"

	WorkloadInstanceTypeValuePodSpec     = "pod_spec"
	WorkloadInstanceTypeValueMiniService = "miniservice"

	NVCAOperatorName = nvcaoptypes.NVCAOperatorName

	BCPACEBackendType = "bcp"

	// objectNames
	NetworkPoliciesConfigmapName              = "nvca-namespace-networkpolicies"
	MiniServiceRBACConfigmapName              = "nvca-miniservice-rbac"
	NVCAConfigmapName                         = "nvca-config"
	NVCAOTelCollectorConfigMapName            = "nvca-otel-collector-config"
	EgressNetworkPolicyNameKey                = "allow-egress-internet-no-internal-no-api"
	IngressNetworkPolicyNameKey               = "allow-ingress-monitoring"
	EgressGXCacheNetworkPolicyNameKey         = "allow-egress-gxcache"
	IngressGXCacheNetworkPolicyNameKey        = "allow-ingress-monitoring-gxcache"
	IngressMonitoringDCGMNetworkPolicyNameKey = "allow-ingress-monitoring-dcgm"
	EgressNVCFCacheAllowPolicyNameKey         = "allow-egress-nvcf-cache"
	EgressCrowdstrikeAllowPolicyNameKey       = "allow-egress-crowdstrike"
	IngressCrowdstrikeNetworkPolicyNameKey    = "allow-ingress-crowdstrike"
	MiniServicesPermissionsRoleName           = "mini-service-restrictions"
	VersionSupportsCSIMountOptions            = "v2.45.0"

	nvcfCustomNetworkPoliciesConfigMapName = "nvcf-custom-network-policies"
	nvcfCustomNetworkPolicyPrefix          = "nvcf-custom-"
	nvcfBackendHelmManagedConfigMapName    = "nvcfbackend-helm-managed"
	nvcfBackendSelfManagedConfigMapName    = "nvcfbackend-self-managed"
	nvcfBackendChartDefaultsConfigMapName  = "nvcfbackend-chart-defaults"

	//nolint:gosec // G101: This is a ConfigMap name, not a credential
	nvcfCustomAnnotationsConfigMapName = "nvca-namespace-pod-annotations"

	// BYOO Prometheus egress policy
	EgressBYOOOTelPrometheusNetworkPolicyNameKey = "allow-egress-prometheus-nvcf-byoo"

	// Webhooks
	//nolint:gosec
	NVCAWebhookTLSCertSecretName = "nvca-webhook-tls-server-certs"
	//nolint:gosec
	NVCAWebhookTLSCASecretName = "nvca-webhook-tls-ca-certs"
	NVCAWebhookContainerName   = "webhook"
	SrvCertsVolumeName         = "webhook-server-certs"
	CACertsVolumeName          = "webhook-ca-certs"
	SrvCertsMountDir           = "/certs/server"
	CACertsMountDir            = "/certs/ca"

	agentConfigDir                = "/var/run/nvca"
	agentConfigFile               = "config.yaml"
	agentConfigFilePath           = agentConfigDir + "/" + agentConfigFile
	agentConfigConfigMapName      = "agent-config"
	agentConfigMergeConfigMapName = "agent-config-merge"
	agentConfigVolumeName         = "agent-config"

	// ReVal config.
	ReValCacheVolumeName = "reval-rendered-helmcharts"
	ReValCacheDir        = agentConfigDir + "/" + ReValCacheVolumeName

	// New preferred secret names for OAuth2/OIDC authentication
	//nolint:gosec
	OAuthClientKeySecretName = "oauth-client-secret-key"
	//nolint:gosec
	OAuthClientKeySecretDataKey = "secretKey"
	//nolint:gosec
	OAuthClientIDSecretName = "oauth-client-id"
	//nolint:gosec
	OAuthClientIDSecretDataKey = "clientID"

	OTELConfigSecretName = "otel-nvca-config"
	//nolint:gosec
	NVCAImagePullSecretName = "nvca-image-pull-secret"
	//nolint:gosec
	OTELExporterSecretKey = "OTEL_EXPORTER"
	//nolint:gosec
	OTELEndpointSecretKey = "OTEL_ENDPOINT"
	//nolint:gosec
	OTELServiceNameKey = "LS_SERVICE_NAME"
	//nolint:gosec
	OTELAccessTokenKey = "LS_ACCESS_TOKEN"
	//nolint:gosec
	NGCServiceAPIKeySecretName = "ngc-service-api-key"
	//nolint:gosec
	NGCServiceAPIKeySecretDataKey = NGCServiceAPIKeySecretName
	//nolint:gosec
	NGCServiceAPIKeyFileEnvVar = "NGC_SERVICE_API_KEY_FILE"
	//nolint:gosec
	NGCAPIKeySecretName    = "ngc-api-key"
	NVCAVaultConfigmapName = "nvca-vault-agent"

	NVCAInternalPersistentStorageConfigJSONBase64Key = "NVCA_INTERNAL_PERSISTENT_STORAGE_CONFIG_JSON_BASE64"
	NVCASharedStorageonfigJSONBase64Key              = "NVCA_SHARED_STORAGE_CONFIG_JSON_BASE64"

	// default params for nvca deployment
	DefaultLogLevel              = "info"
	DefaultNVCASystemNamespace   = nvcaoptypes.DefaultNVCASystemNamespace
	DefaultNVCARequestsNamespace = nvcaoptypes.DefaultNVCARequestsNamespace
	//nolint:gosec
	DefaultVaultSecretFilePath                         = "/home/nvca/vault-agent/secrets"
	DefaultBackendTypeK8s                              = "k8s"
	DefaultOTELExporter        nvcaconfig.OTELExporter = "lightstep"

	// Ports
	DefaultNVCAListenPortHTTP       = int32(8000)
	DefaultNVCAAdminPortHTTP        = int32(8001)
	DefaultWebhooksListenPortHTTP   = int32(8443)
	DefaultWebhooksServicePortHTTPS = DefaultWebhooksListenPortHTTP

	NVCAOTelCollectorHealthCheckPort = int32(13133)
	NVCAOTelCollectorMetricsPort     = int32(8888)

	// NVCA OTel collector container constants
	NVCAOTelCollectorContainerName       = "nvca-otel-collector"
	NVCAOTelCollectorConfigMountPath     = "/etc/otelcol"
	NVCAOTelCollectorHealthCheckPortName = "otel-health"  // max 15 chars for k8s port names
	NVCAOTelCollectorMetricsPortName     = "otel-metrics" // max 15 chars for k8s port names

	// NVCA OTel collector environment variable names
	NVCAOTelCollectorRequestsNamespaceEnvVar     = "NVCA_OTEL_COLLECTOR_REQUESTS_NAMESPACE"
	NVCAOTelCollectorHealthCheckPortEnvVar       = "NVCA_OTEL_COLLECTOR_HEALTH_CHECK_PORT"
	NVCAOTelCollectorFNDSEndpointEnvVar          = "NVCA_OTEL_COLLECTOR_FNDS_ENDPOINT"
	NVCAOTelCollectorMetricsPortEnvVar           = "NVCA_OTEL_COLLECTOR_METRICS_PORT"
	NVCAOTelCollectorMemoryLimitPercentageEnvVar = "NVCA_OTEL_COLLECTOR_MEMORY_LIMIT_PERCENTAGE"
	NVCAOTelCollectorSpikeLimitPercentageEnvVar  = "NVCA_OTEL_COLLECTOR_SPIKE_LIMIT_PERCENTAGE"
	NVCAOTelCollectorOAuthClientIDEnvVar         = "NVCA_OTEL_COLLECTOR_OAUTH_CLIENT_ID"
	NVCAOTelCollectorOAuthClientSecretFileEnvVar = "NVCA_OTEL_COLLECTOR_OAUTH_CLIENT_SECRET_FILE"
	NVCAOTelCollectorOAuthTokenURLEnvVar         = "NVCA_OTEL_COLLECTOR_OAUTH_TOKEN_URL"
	NVCAOTelCollectorAuthenticatorEnvVar         = "NVCA_OTEL_COLLECTOR_AUTHENTICATOR"

	// NVCA OTel collector memory limiter default percentages
	NVCAOTelCollectorMemoryLimitPercentage = 85
	NVCAOTelCollectorSpikeLimitPercentage  = 15
)

var (
	PriorityClassOptions = []string{"system-node-critical", "system-cluster-critical"}
)

func getSystemNamespace(nb *nvidiaiov1.NVCFBackend) string {
	systemNamespace := DefaultNVCASystemNamespace
	if nb.Spec.ClusterConfig.SystemNamespace != "" {
		systemNamespace = nb.Spec.ClusterConfig.SystemNamespace
	}

	return systemNamespace
}

func getRequestsNamespace(nb *nvidiaiov1.NVCFBackend) string {
	requestsNamespace := DefaultNVCARequestsNamespace
	if nb.Spec.ClusterConfig.RequestsNamespace != "" {
		requestsNamespace = nb.Spec.ClusterConfig.RequestsNamespace
	}

	return requestsNamespace
}

func makeWorkloadNamespaceLabelSelectors(icmsInstanceTypes ...string) map[string][]string {
	labels := getAppLabels()
	selVals := make(map[string][]string, len(labels))
	selVals[nvcatypes.WorkloadInstanceTypeLabel] = icmsInstanceTypes
	return selVals
}

func boolPtr(b bool) *bool { return &b }

func (bc *BackendK8sCache) setupRequestsNamespace(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	requestsNamespace := getRequestsNamespace(nb)

	labels := map[string]string{
		// This namespace will be reconciled by the ICMSRequest controller in NVCA
		// so must be managed by it.
		ManagedbyLabelKey:                   nvcaoptypes.NVCAModuleName,
		nvcatypes.WorkloadInstanceTypeLabel: WorkloadInstanceTypeValuePodSpec,
	}

	reqNSObj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   requestsNamespace,
			Labels: labels,
		},
	}

	// set the labels for gxcache if enabled
	if bc.enableGXCache {
		if _, ok := reqNSObj.Labels[clustermgmt.ShaderCacheLabelKey]; !ok {
			if reqNSObj.Labels == nil {
				reqNSObj.Labels = make(map[string]string)
			}
			reqNSObj.Labels[clustermgmt.ShaderCacheLabelKey] = strconv.FormatBool(true)
		}
	} else {
		if _, ok := reqNSObj.Labels[clustermgmt.ShaderCacheLabelKey]; !ok {
			delete(reqNSObj.Labels, clustermgmt.ShaderCacheLabelKey)
		}
	}

	if err := bc.createOrUpdateNamespace(ctx, reqNSObj); err != nil {
		return fmt.Errorf("failed to setup namespace %v", reqNSObj.Name)
	}

	defaultSA := &corev1.ServiceAccount{
		AutomountServiceAccountToken: boolPtr(false),
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: requestsNamespace,
		},
	}

	if err := bc.createOrUpdateServiceAccount(ctx, defaultSA); err != nil {
		return fmt.Errorf("failed to update ServiceAccount %s/%s, error: %v", defaultSA.Namespace, defaultSA.Name, err)
	}

	return nil
}

func (bc *BackendK8sCache) setupSystemNamespace(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	systemNamespace := getSystemNamespace(nb)

	sysNSObj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        systemNamespace,
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
	}

	if err := bc.createOrUpdateNamespace(ctx, sysNSObj); err != nil {
		return fmt.Errorf("failed to setup namespace %s: %v", sysNSObj.Name, err)
	}

	// setup resource quota on systemNamespace
	rq := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:        systemNamespace,
			Namespace:   systemNamespace,
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		Spec: corev1.ResourceQuotaSpec{
			ScopeSelector: &corev1.ScopeSelector{
				MatchExpressions: []corev1.ScopedResourceSelectorRequirement{
					{
						Operator:  corev1.ScopeSelectorOpIn,
						ScopeName: corev1.ResourceQuotaScopePriorityClass,
						Values:    PriorityClassOptions,
					},
				},
			},
		},
	}

	if err := bc.createOrUpdateResourceQuota(ctx, rq); err != nil {
		return fmt.Errorf("failed to setup resourceQuota for namespace %s: %v", sysNSObj.Name, err)
	}

	return nil
}

// isNVCAVersion251OrNewer checks if the NVCA version is 2.51.0 or newer.
// This is used by version-boundary tests and compatibility helpers.
func isNVCAVersion251OrNewer(version string) bool {
	if version == "" {
		return false
	}
	version = strings.TrimPrefix(version, "v")

	// Strip any suffix (e.g., -rc1, -dev) before parsing
	// Examples: 2.51.0-rc1 → 2.51.0, 2.51.0-dev → 2.51.0
	if idx := strings.Index(version, "-"); idx != -1 {
		version = version[:idx]
	}

	// Parse version format: MAJOR.MINOR.PATCH
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}

	if major > 2 || major == 2 && minor >= 51 {
		return true
	}
	return false
}

// getClientSecretsEnvFile returns the vault secrets file path.
func getClientSecretsEnvFile(vaultSecretFilePath, _ string) string {
	return fmt.Sprintf("%s/%s", vaultSecretFilePath, "oauth-client-secrets.env")
}

// getOAuthEnvVars returns environment variables for NVCA OAuth authentication.
// Always uses OAUTH_CLIENT_* variables (from nvcf-go) - NVCA has built-in backwards compatibility.
// When Vault is enabled, credentials come from ClientSecretsEnvFile (Vault agent output) - do not
// add SecretKeyRef env vars since setupOAuthClientSecrets skips creating those secrets.
func getOAuthEnvVars(nb *nvidiaiov1.NVCFBackend, oauthConfig nvidiaiov1.OAuthConfig) []corev1.EnvVar {
	if oauthConfig.ClientID == "" {
		return nil
	}
	if nb.Spec.VaultConfig.Enabled {
		return nil
	}

	// Always use OAUTH_CLIENT_* variables (NVCA handles backwards compatibility internally)
	envVars := []corev1.EnvVar{
		{
			Name: auth.ClientIDEnv,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: OAuthClientIDSecretName,
					},
					Key: OAuthClientIDSecretDataKey,
				},
			},
		},
	}

	// Only add secret key env var if vault is not enabled
	if !nb.Spec.VaultConfig.Enabled && oauthConfig.ClientSecretKey != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name: auth.ClientSecretEnv,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: OAuthClientKeySecretName,
					},
					Key: OAuthClientKeySecretDataKey,
				},
			},
		})
	}

	return envVars
}

// envVarsFromMap converts a name->value map into EnvVars with literal Value (for overrideEnvironmentVars).
// Keys are sorted so that the same map produces stable Env order.
func envVarsFromMap(m map[string]string) []corev1.EnvVar {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]corev1.EnvVar, 0, len(keys))
	for _, k := range keys {
		out = append(out, corev1.EnvVar{Name: k, Value: m[k]})
	}
	return out
}

// getOAuthConfig returns the OAuth configuration.
func getOAuthConfig(nb *nvidiaiov1.NVCFBackend) nvidiaiov1.OAuthConfig {
	if nb.Spec.OAuthConfig.ClientID != "" {
		return nb.Spec.OAuthConfig
	}

	return nvidiaiov1.OAuthConfig{}
}

// getOAuthClientMountPath returns the vault mount path for OAuth client secrets.
func getOAuthClientMountPath(nb *nvidiaiov1.NVCFBackend) string {
	return nb.Spec.VaultConfig.OAuthClientMountPath
}

func (bc *BackendK8sCache) validateNVCFBackendParams(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	log := core.GetLogger(ctx)

	oauthConfig := getOAuthConfig(nb)

	if nb.Spec.VaultConfig.Enabled && oauthConfig.ClientSecretKey != "" {
		return fmt.Errorf("vault is enabled. ClientSecretKey cannot be specified")
	}

	if !nb.Spec.VaultConfig.Enabled && oauthConfig.ClientID != "" && oauthConfig.ClientSecretKey == "" {
		return fmt.Errorf("vault-integration disabled. ClientSecretKey cannot be empty")
	}

	if cfg := nb.Spec.ClusterConfig.GPUDiscovery.Static; cfg != nil {
		if cfg.AllocatedGPUCapacity == 0 {
			return fmt.Errorf("allocated GPU capacity cannot be 0 when static GPU discovery is enabled")
		}
	}

	if IsSelfHosted(nb) {
		if nb.Spec.ClusterConfig.ClusterID == "" {
			return fmt.Errorf("clusterID is required for self-managed clusters but was empty")
		}
		if nb.Spec.ClusterConfig.ClusterGroupID == "" {
			return fmt.Errorf("clusterGroupID is required for self-managed clusters but was empty")
		}
	}

	// Check if required ConfigMaps exist
	requiredConfigMaps := []string{
		nvcfCustomAnnotationsConfigMapName,
	}

	for _, cmName := range requiredConfigMaps {
		_, err := bc.clients.K8s.CoreV1().ConfigMaps(nb.Namespace).Get(ctx, cmName, metav1.GetOptions{})
		if err != nil {
			if k8serr.IsNotFound(err) {
				log.Errorf("required ConfigMap %s not found in namespace %s", cmName, nb.Namespace)
				return fmt.Errorf("required ConfigMap %s not found in namespace %s", cmName, nb.Namespace)
			}
			return fmt.Errorf("failed to get ConfigMap %s: %w", cmName, err)
		}
	}

	return nil
}

func (bc *BackendK8sCache) setupNVCAAgentInfra(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	var err error
	log := core.GetLogger(ctx)
	log.Infof("setting-up NVCAAgent infra %v", nvcaoptypes.NVCAModuleName)

	err = bc.validateNVCFBackendParams(ctx, nb)
	if err != nil {
		return fmt.Errorf("failed to sync: %w", err)
	}

	err = bc.setupSystemNamespace(ctx, nb)
	if err != nil {
		return fmt.Errorf("failed to setup system namespace for NVCFBackend %v/%v, err: %w",
			nb.Namespace, nb.Name, err)
	}
	err = bc.setupRequestsNamespace(ctx, nb)
	if err != nil {
		return fmt.Errorf("failed to setup requests namespace for NVCFBackend %v/%v, err: %w",
			nb.Namespace, nb.Name, err)
	}

	err = bc.setupImagePullSecrets(ctx, nb)
	if err != nil {
		return fmt.Errorf("failed to setup %v for NVCFBackend %v/%v, err: %w", NVCAImagePullSecretName,
			nb.Namespace, nb.Name, err)
	}

	agentCfg, err := bc.newAgentConfig(ctx, nb)
	if err != nil {
		return err
	}
	if err := bc.setupAgentConfigConfigMap(ctx, nb, agentCfg); err != nil {
		return fmt.Errorf("failed to setup agent config ConfigMap: %w", err)
	}

	// Setup OAuth client secrets.
	err = bc.setupOAuthClientSecrets(ctx, nb)
	if err != nil {
		return fmt.Errorf("failed to setup auth client secrets for NVCFBackend %v/%v, err: %w",
			nb.Namespace, nb.Name, err)
	}

	err = bc.setupOTELConfigSecret(ctx, nb)
	if err != nil {
		return fmt.Errorf("failed to setup %v for NVCFBackend %v/%v, err: %w", OTELConfigSecretName,
			nb.Namespace, nb.Name, err)
	}

	webhookCert, err := generateWebhookCerts(nb, bc.now())
	if err != nil {
		return fmt.Errorf("failed to create webhookCerts, err: %w", err)
	}

	if err := bc.setupWebhookSecrets(ctx, nb, webhookCert); err != nil {
		return fmt.Errorf("failed to create webhook secrets, err: %w", err)
	}

	err = bc.setupStaticGPUConfigMap(ctx, nb)
	if err != nil {
		return fmt.Errorf("failed to setup %v for NVCFBackend %v/%v, err: %w", NVCAConfigmapName,
			nb.Namespace, nb.Name, err)
	}

	err = bc.setupNGCServiceAPIKeySecret(ctx, nb)
	if err != nil {
		return fmt.Errorf("failed to setup %v for NVCFBackend %v/%v, err: %w", NGCServiceAPIKeySecretName,
			nb.Namespace, nb.Name, err)
	}

	err = bc.setupOTelCollectorConfigMap(ctx, nb)
	if err != nil {
		return fmt.Errorf("failed to setup %v for NVCFBackend %v/%v, err: %w", NVCAOTelCollectorConfigMapName,
			nb.Namespace, nb.Name, err)
	}

	err = bc.setupNetworkPoliciesConfigMap(ctx, nb)
	if err != nil {
		return fmt.Errorf("failed to setup %v for NVCFBackend %v/%v, err: %w", NetworkPoliciesConfigmapName,
			nb.Namespace, nb.Name, err)
	}

	err = bc.mirrorConfigMap(ctx, nb, nvcfCustomAnnotationsConfigMapName)
	if err != nil {
		return fmt.Errorf("failed to setup %v for NVCFBackend %v/%v, err: %w", nvcfCustomAnnotationsConfigMapName,
			nb.Namespace, nb.Name, err)
	}

	err = bc.setupVaultConfigmap(ctx, nb)
	if err != nil {
		return fmt.Errorf("failed to setup %v for NVCFBackend %v/%v, err: %w", NVCAVaultConfigmapName,
			nb.Namespace, nb.Name, err)
	}

	err = bc.setupNVCARBAC(ctx, nb)
	if err != nil {
		return fmt.Errorf("failed to setup RBAC for NVCFBackend %v/%v, err: %w",
			nb.Namespace, nb.Name, err)
	}

	if err := bc.setupNVCAService(ctx, nb); err != nil {
		return fmt.Errorf("failed to setup NVCA service for NVCFBackend %v/%v, err: %w",
			nb.Namespace, nb.Name, err)
	}

	if err := bc.setupNVCAMiniServiceInfra(ctx, nb, webhookCert); err != nil {
		return fmt.Errorf("failed setupNVCAMiniServiceInfra for NVCFBackend %v/%v, err: %w",
			nb.Namespace, nb.Name, err)
	}

	if err := bc.setupNVCAMutatingWebhookConfiguration(ctx, nb, webhookCert); err != nil {
		return fmt.Errorf("failed setupNVCAMutatingWebhookConfiguration for NVCFBackend %v/%v, err: %w",
			nb.Namespace, nb.Name, err)
	}

	err = bc.setupNVCADeployment(ctx, nb)
	if err != nil {
		return fmt.Errorf("failed to setup NVCA deployment for NVCFBackend %v/%v, err: %w",
			nb.Namespace, nb.Name, err)
	}

	log.Infof("setting-up NVCAAgent infra %v complete", nvcaoptypes.NVCAModuleName)

	return nil
}

// getNVCAFeatureFlags returns a comma-separated list of feature flags. Disabled feature flags are prefixed with '-'.
// A feature flag that appears in the values list both as enabled and disabled will be disabled.
func (bc *BackendK8sCache) getNVCAFeatureFlags(nb *nvidiaiov1.NVCFBackend) string {
	disabledFlags := sets.New[string]()
	enabledFlags := sets.New[string]()
	for _, v := range nb.Spec.FeatureGate.Values {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		switch v[0] {
		case '-':
			disabledFlags.Insert(v[1:])
		case '+':
			enabledFlags.Insert(v[1:])
		default:
			enabledFlags.Insert(v)
		}
	}
	bothEnabledAndDisabledFlags := enabledFlags.Intersection(disabledFlags)
	ffSet := enabledFlags.Difference(disabledFlags).Clone()

	for _, disabledFF := range disabledFlags.Difference(enabledFlags).UnsortedList() {
		ffSet.Insert("-" + disabledFF)
	}

	for _, bothEnabledAndDisabledFF := range bothEnabledAndDisabledFlags.UnsortedList() {
		ffSet.Insert("-" + bothEnabledAndDisabledFF)
	}

	if nb.Spec.ClusterConfig.FNDService.IsEnabled(ffSet.UnsortedList()) {
		ffSet.Insert(featureflag.UseFunctionDeploymentStages.Key)
	}

	if bc.enableGXCache {
		ffSet.Insert(featureflag.GXCache.Key)
	}

	if IsSelfHosted(nb) {
		ffSet.Insert(featureflag.SelfHosted.Key)
	}

	ffs := ffSet.UnsortedList()
	sort.Strings(ffs)
	return strings.Join(ffs, ",")
}

func (bc *BackendK8sCache) setupNGCServiceAPIKeySecret(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	ngcServiceAPIStr, err := bc.ngcServiceKeyFetcher.FetchToken(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch NGC Service API key, err: %v", err)
	}

	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        NGCServiceAPIKeySecretName,
			Namespace:   getSystemNamespace(nb),
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		Data: map[string][]byte{
			NGCServiceAPIKeySecretDataKey: []byte(ngcServiceAPIStr),
		},
	}
	return bc.createOrUpdateSecret(ctx, s)
}

func (bc *BackendK8sCache) setupOTELConfigSecret(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	log := core.GetLogger(ctx)
	if nb.Spec.FeatureGate.OTELConfig == nil {
		log.Infof("OTELConfig not specified, skip setup")
		return nil
	}
	if nb.Spec.FeatureGate.OTELConfig.AccessToken != "" && nb.Spec.FeatureGate.OTELConfig.Endpoint == "" {
		return fmt.Errorf("otelConfig.endpoint is required when otelConfig.accessToken is set")
	}
	exporter := string(DefaultOTELExporter)
	if nb.Spec.FeatureGate.OTELConfig.Exporter != "" {
		exporter = nb.Spec.FeatureGate.OTELConfig.Exporter
	}
	accessToken := []byte(nb.Spec.FeatureGate.OTELConfig.AccessToken)
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        OTELConfigSecretName,
			Namespace:   getSystemNamespace(nb),
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		Data: map[string][]byte{
			OTELExporterSecretKey:               []byte(exporter),
			OTELEndpointSecretKey:               []byte(nb.Spec.FeatureGate.OTELConfig.Endpoint),
			OTELServiceNameKey:                  []byte(nb.Spec.FeatureGate.OTELConfig.ServiceName),
			OTELAccessTokenKey:                  accessToken,
			"NVCA_AUTHZ_LIGHTSTEP_ACCESS_TOKEN": accessToken,
		},
	}
	return bc.createOrUpdateSecret(ctx, s)
}

func (bc *BackendK8sCache) setupNVCARBAC(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	log := core.GetLogger(ctx)
	var err error
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        nvcaoptypes.NVCAModuleName,
			Namespace:   getSystemNamespace(nb),
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		AutomountServiceAccountToken: boolPtr(false),
		ImagePullSecrets:             getImagePullSecretReferences(ctx, nb, bc.generateImagePullSecret, bc.additionalImagePullSecrets),
	}

	err = bc.createOrUpdateServiceAccount(ctx, sa)
	if err != nil {
		log.WithError(err).Errorf("failed to setup ServiceAccount %v/%v", sa.Namespace, sa.Name)
		return err
	}

	readOnlyVerbs := []string{"get", "list", "watch"}
	crudVerbs := []string{"get", "list", "watch", "create", "update", "delete", "patch"}
	crudWithCollectionVerbs := []string{"get", "list", "watch", "create", "update", "delete", "deletecollection", "patch"}

	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:        nvcaoptypes.NVCAModuleName,
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"secrets", "configmaps"},
				Verbs:     crudVerbs,
			},
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumes"},
				Verbs:     crudWithCollectionVerbs,
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{"icmsrequests"},
				Verbs:     crudVerbs,
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{"icmsrequests/status"},
				Verbs:     []string{"get", "update", "patch"},
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{"storagerequests", "storagerequests/status"},
				Verbs:     crudVerbs,
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"storageclasses", "volumeattachments"},
				Verbs:     crudWithCollectionVerbs,
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"csidrivers"},
				Verbs:     readOnlyVerbs,
			},
			{
				APIGroups:     []string{"security.openshift.io"},
				Resources:     []string{"securitycontextconstraints"},
				ResourceNames: []string{"nonroot"},
				Verbs:         []string{"use"},
			},
			{
				APIGroups:     []string{"admissionregistration.k8s.io"},
				Resources:     []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"},
				ResourceNames: []string{nvcaoptypes.NVCAModuleName},
				Verbs:         []string{"get", "list", "watch"},
			},
		},
	}

	if nb.Spec.ClusterConfig.GPUDiscovery.Static == nil {
		cr.Rules = append(cr.Rules, rbacv1.PolicyRule{
			APIGroups: []string{""},
			Resources: []string{"nodes"},
			// Update/patch are needed for NVCA's node label updater.
			Verbs: []string{"get", "list", "watch", "update", "patch"},
		})
	} else {
		cr.Rules = append(cr.Rules, rbacv1.PolicyRule{
			APIGroups: []string{""},
			Resources: []string{"nodes"},
			// Nodes are read-only in static mode.
			Verbs: readOnlyVerbs,
		})
	}

	if strings.EqualFold(getBackendType(nb), DefaultBackendTypeK8s) {
		// setting-up for k8s only
		k8sPolicyRuleList := []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{
					"serviceaccounts",
					"namespaces",
					"pods",
					"pods/log",
					"pods/status",
					"events",
					"services",
					"persistentvolumes",
					"persistentvolumes/status",
					"persistentvolumeclaims",
					"persistentvolumeclaims/status",
					"resourcequotas",
				},
				Verbs: crudVerbs,
			},
			{
				APIGroups: []string{""},
				Resources: []string{"serviceaccounts"},
				Verbs:     []string{"impersonate"},
			},
			{
				APIGroups: []string{"batch"},
				Resources: []string{"jobs", "cronjobs"},
				Verbs:     crudVerbs,
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"replicasets", "deployments", "statefulsets"},
				Verbs:     crudVerbs,
			},
			{
				APIGroups: []string{"networking.k8s.io"},
				Resources: []string{"networkpolicies"},
				Verbs:     crudWithCollectionVerbs,
			},
			{
				APIGroups: []string{"node.k8s.io"},
				Resources: []string{"runtimeclasses"},
				Verbs:     readOnlyVerbs,
			},
			{
				APIGroups: []string{"rbac.authorization.k8s.io"},
				Resources: []string{"roles", "rolebindings"},
				Verbs:     crudVerbs,
			},
			{
				APIGroups: []string{"coordination.k8s.io"},
				Resources: []string{"leases"},
				Verbs:     crudVerbs,
			},
			// required for fetching run:ai queues
			{
				APIGroups: []string{"scheduling.run.ai"},
				Resources: []string{"queues"},
				Verbs:     readOnlyVerbs,
			},
			{
				APIGroups: []string{"nvca.nvcf.nvidia.io"},
				Resources: []string{"miniservices", "miniservices/status"},
				Verbs:     crudVerbs,
			},
			{
				APIGroups: []string{"run.ai"},
				Resources: []string{"kartas"},
				Verbs:     readOnlyVerbs,
			},
		}

		if slices.Contains(nb.Spec.ClusterConfig.Attributes, featureflag.AttrNVLinkOptimized.Key+"=true") {
			k8sPolicyRuleList = append(k8sPolicyRuleList,
				rbacv1.PolicyRule{
					APIGroups: []string{"resource.nvidia.com"},
					Resources: []string{"computedomains"},
					Verbs:     crudWithCollectionVerbs,
				},
				rbacv1.PolicyRule{
					APIGroups: []string{"resource.k8s.io"},
					Resources: []string{"resourceclaims", "resourceclaimtemplates"},
					Verbs:     crudWithCollectionVerbs,
				},
				rbacv1.PolicyRule{
					APIGroups: []string{"resource.k8s.io"},
					Resources: []string{"deviceclasses", "resourceslices"},
					Verbs:     readOnlyVerbs,
				},
				rbacv1.PolicyRule{
					APIGroups: []string{"apps"},
					Resources: []string{"daemonsets"},
					Verbs:     readOnlyVerbs,
				},
			)
		}

		cfg, foundCfg, err := bc.getAgentConfigToMerge(ctx)
		if err != nil {
			log.WithError(err).Error("Failed to get NVCA merge config")
			return err
		}
		if foundCfg && cfg.Cluster.ValidationPolicy != nil {
			for _, kt := range cfg.Cluster.ValidationPolicy.AllowedExtraKubernetesTypes {
				k8sPolicyRuleList = append(k8sPolicyRuleList, rbacv1.PolicyRule{
					APIGroups: []string{kt.Group},
					Resources: []string{kt.Resource},
					Verbs:     crudVerbs,
				})
			}
		}

		cr.Rules = append(cr.Rules, k8sPolicyRuleList...)
	}
	err = bc.createOrUpdateClusterRole(ctx, cr)
	if err != nil {
		log.WithError(err).Errorf("failed to setup ClusterRole %v", cr.Name)
		return err
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        nvcaoptypes.NVCAModuleName,
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      sa.Name,
				Namespace: sa.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			Name:     nvcaoptypes.NVCAModuleName,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}
	err = bc.createOrUpdateClusterRoleBinding(ctx, crb)
	if err != nil {
		log.WithError(err).Errorf("failed to setup ClusterRoleBinding %v", crb.Name)
		return err
	}

	return nil
}

func (bc *BackendK8sCache) mirrorConfigMap(ctx context.Context, nb *nvidiaiov1.NVCFBackend, srcName string) error {
	log := core.GetLogger(ctx)

	srcCM, err := bc.clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Get(ctx, srcName, metav1.GetOptions{})
	if err != nil {
		log.Errorf("failed to get source configmap %v", srcName)
		return err
	}

	cmTemplate := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      srcName,
			Namespace: getSystemNamespace(nb),
		},
		Data: srcCM.Data,
	}
	return bc.createOrUpdateConfigMap(ctx, &cmTemplate)
}

func (bc *BackendK8sCache) setupNetworkPoliciesConfigMap(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	log := core.GetLogger(ctx)

	netPolData, err := bc.getNetworkPoliciesData(ctx, nb)
	if err != nil {
		return err
	}

	customNetPolData, err := bc.getCustomNetworkPoliciesData(ctx, bc.clients.K8s.CoreV1().ConfigMaps(bc.operatorNamespace).Get)
	if err != nil {
		return err
	}

	mergedNetPolData, collisions := mergeMapsNoOverwrites(netPolData, customNetPolData)
	if len(collisions) > 0 {
		log.Errorf("custom network policies collision(s) detected: %v", collisions)
		return fmt.Errorf("custom network policies collision(s) detected: %v", collisions)
	}

	ec := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        NetworkPoliciesConfigmapName,
			Namespace:   getSystemNamespace(nb),
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		Data: mergedNetPolData,
	}

	return bc.createOrUpdateConfigMap(ctx, ec)
}

func (bc *BackendK8sCache) getCustomNetworkPoliciesData(
	ctx context.Context,
	configMapGetter func(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.ConfigMap, error),
) (map[string]string, error) {
	log := core.GetLogger(ctx)
	customNetPolData := map[string]string{}

	// Try to get the custom network policies configmap
	cm, err := configMapGetter(ctx, nvcfCustomNetworkPoliciesConfigMapName, metav1.GetOptions{})
	if err != nil {
		if k8serr.IsNotFound(err) {
			log.Debugf("Custom network policies configmap %s not found, skipping", nvcfCustomNetworkPoliciesConfigMapName)
			return customNetPolData, nil
		}
		return nil, fmt.Errorf("failed to get custom network policies configmap %s: %w", nvcfCustomNetworkPoliciesConfigMapName, err)
	}

	var errs []error
	// Add all policies from the configmap
	for key, value := range cm.Data {
		if strings.TrimSpace(value) == "" {
			log.Debugf("Custom network policy %s is empty, skipping", key)
			continue
		}

		// Parse the policy to a network policy object
		netPol := &netv1.NetworkPolicy{}
		err := yaml.Unmarshal([]byte(value), netPol)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to parse custom network policy %s: %w", key, err))
			continue
		}

		netPolName := netPol.Name
		if !strings.HasPrefix(netPolName, nvcfCustomNetworkPolicyPrefix) {
			netPolName = nvcfCustomNetworkPolicyPrefix + netPolName
		}

		// Check for name collisions
		if _, ok := customNetPolData[netPolName]; ok {
			errs = append(errs, fmt.Errorf("custom network policy name collision: %s defined multiple times", netPolName))
			continue
		}

		// Verify the network policy is valid by attempting a dry-run create
		if err := bc.validateNetworkPolicy(ctx, netPol); err != nil {
			errs = append(errs, fmt.Errorf("custom network policy %s validation failed: %w", key, err))
			continue
		}

		// No collisions, add the policy to the map
		customNetPolData[netPolName] = value
	}

	// Return the first error if there are any
	if len(errs) > 0 {
		err := errors.Join(errs...)
		log.WithError(err).Error("failed to parse custom network policies")
		return nil, nvcaoperatorerrors.FatalError(err)
	}

	return customNetPolData, nil
}

// validateNetworkPolicy validates a NetworkPolicy against the Kubernetes API using dry-run
func (bc *BackendK8sCache) validateNetworkPolicy(ctx context.Context, netPol *netv1.NetworkPolicy) error {
	log := core.GetLogger(ctx)

	// Set a temporary namespace if not specified for validation
	netPolToValidate := netPol.DeepCopy()
	if netPolToValidate.Namespace == "" {
		netPolToValidate.Namespace = "default"
	}

	// Attempt to create the NetworkPolicy with dry-run to validate it
	_, err := bc.clients.K8s.NetworkingV1().NetworkPolicies(netPolToValidate.Namespace).Create(
		ctx,
		netPolToValidate,
		metav1.CreateOptions{
			DryRun: []string{metav1.DryRunAll},
		},
	)

	if err != nil {
		log.Debugf("NetworkPolicy validation failed for %s: %v", netPol.Name, err)
		return fmt.Errorf("NetworkPolicy validation failed: %w", err)
	}

	log.Debugf("NetworkPolicy %s validated successfully", netPol.Name)
	return nil
}

func mergeMapsNoOverwrites(base map[string]string, add map[string]string) (map[string]string, []string) {
	if base == nil {
		return add, []string{}
	}
	collisions := []string{}
	for k, v := range add {
		if _, ok := base[k]; ok {
			collisions = append(collisions, k)
		} else {
			base[k] = v
		}
	}
	return base, collisions
}

func (bc *BackendK8sCache) setupVaultConfigmap(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	log := core.GetLogger(ctx)
	if !nb.Spec.VaultConfig.Enabled {
		log.Infof("skip setting-up %v configMap since Vault is disabled", NVCAVaultConfigmapName)
		return nil
	}
	ec := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        NVCAVaultConfigmapName,
			Namespace:   getSystemNamespace(nb),
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		Data: getVaultConfigData(nb),
	}

	return bc.createOrUpdateConfigMap(ctx, ec)
}

func (bc *BackendK8sCache) setupStaticGPUConfigMap(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	log := core.GetLogger(ctx)

	staticCfg := nb.Spec.ClusterConfig.GPUDiscovery.Static
	if staticCfg == nil {
		log.Debug("Static GPU discovery is disabled, skipping NVCA ConfigMap creation")
		return nil
	}

	// If remote GPU config is specified use that first, and then fall back to the
	// local config map
	var cmData map[string]string
	if staticCfg.GPUConfig == "" {
		srcConfigMapName := "nvca-static-gpus"
		if staticCfg.ConfigMapName != "" {
			srcConfigMapName = staticCfg.ConfigMapName
		}

		srcCM, err := bc.clients.K8s.CoreV1().ConfigMaps(nb.Namespace).Get(ctx, srcConfigMapName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get static GPU config source ConfigMap %s/%s: %v", nb.Namespace, srcConfigMapName, err)
		}
		cmData = srcCM.Data
	} else {
		cmData = map[string]string{
			"gpus": staticCfg.GPUConfig,
		}
	}

	nc := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        NVCAConfigmapName,
			Namespace:   getSystemNamespace(nb),
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		Data: cmData,
	}

	return bc.createOrUpdateConfigMap(ctx, nc)
}

// setupOAuthClientSecrets creates OAuth client secrets.
func (bc *BackendK8sCache) setupOAuthClientSecrets(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	log := core.GetLogger(ctx)
	oauthConfig := getOAuthConfig(nb)

	if nb.Spec.VaultConfig.Enabled {
		log.Infof("skip setting-up OAuth client secrets since vault is enabled")
		return nil
	}

	clientIDSecretName := OAuthClientIDSecretName
	clientKeySecretName := OAuthClientKeySecretName

	// Create client secret key.
	if oauthConfig.ClientSecretKey != "" {
		clientKeySecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        clientKeySecretName,
				Namespace:   getSystemNamespace(nb),
				Annotations: getNBAnnotations(nb),
				Labels:      getAppLabels(),
			},
			Data: map[string][]byte{
				OAuthClientKeySecretDataKey: []byte(oauthConfig.ClientSecretKey),
			},
		}
		if err := bc.createOrUpdateSecret(ctx, clientKeySecret); err != nil {
			return fmt.Errorf("failed to create %s secret: %w", clientKeySecretName, err)
		}
	}

	// Create client ID secret with version-appropriate name
	if oauthConfig.ClientID != "" {
		clientIDSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        clientIDSecretName,
				Namespace:   getSystemNamespace(nb),
				Annotations: getNBAnnotations(nb),
				Labels:      getAppLabels(),
			},
			Data: map[string][]byte{
				OAuthClientIDSecretDataKey: []byte(oauthConfig.ClientID),
			},
		}
		if err := bc.createOrUpdateSecret(ctx, clientIDSecret); err != nil {
			return fmt.Errorf("failed to create %s secret: %w", clientIDSecretName, err)
		}
	} else if nb.Spec.VaultConfig.Enabled {
		return fmt.Errorf("ClientID cannot be empty when vault is enabled")
	}

	return nil
}

// setupOAuthClientIDSecret is deprecated. Use setupOAuthClientSecrets instead.
// Kept for backward compatibility.
func (bc *BackendK8sCache) setupOAuthClientIDSecret(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	// Delegate to new function
	return bc.setupOAuthClientSecrets(ctx, nb)
}

func (bc *BackendK8sCache) setupAgentConfigConfigMap(
	ctx context.Context,
	nb *nvidiaiov1.NVCFBackend,
	cfg nvcaconfig.Config,
) error {
	mergeCfg, _, err := bc.getAgentConfigToMerge(ctx)
	if err != nil {
		return fmt.Errorf("get agent config to merge: %w", err)
	}

	cb, err := encodeAgentConfig(cfg, mergeCfg, nb.Spec.AgentConfig.NATSURL, agentHostOverrideConfig(nb, bc.envType))
	if err != nil {
		return fmt.Errorf("encode config: %v", err)
	}

	s := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        agentConfigConfigMapName,
			Namespace:   getSystemNamespace(nb),
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		Data: map[string]string{
			agentConfigFile: string(cb),
		},
	}

	return bc.createOrUpdateConfigMap(ctx, s)
}

type agentHostOverrides struct {
	ICMSHostHeaderOverride             string
	HelmReValServiceHostHeaderOverride string
	NATSHostOverride                   *string
}

func agentHostOverrideConfig(nb *nvidiaiov1.NVCFBackend, envType nvidiaiov1.EnvType) agentHostOverrides {
	miniService := nb.Spec.ClusterConfig.MiniService.Complete(envType)
	reValHost := miniService.HelmReValServiceHostHeaderOverride
	if usesSelfHostedColocatedReVal(nb) {
		reValHost = ""
	}
	return agentHostOverrides{
		ICMSHostHeaderOverride:             nb.Spec.ICMSConfig.ICMSServiceHostHeaderOverride,
		HelmReValServiceHostHeaderOverride: reValHost,
		NATSHostOverride:                   nb.Spec.AgentConfig.NATSHostOverride,
	}
}

func encodeAgentConfig(cfg nvcaconfig.Config, mergeCfg nvcaconfig.Config, natsURL *string, hostOverrides agentHostOverrides) ([]byte, error) {
	if natsURL != nil && *natsURL != "" {
		mergeCfg.Agent.NATSURL = *natsURL
	}

	b, err := nvcaconfig.EncodeConfig(cfg, mergeCfg)
	if err != nil {
		return nil, err
	}

	return applyAgentHostOverrides(b, hostOverrides)
}

func applyAgentHostOverrides(data []byte, hostOverrides agentHostOverrides) ([]byte, error) {
	if hostOverrides.ICMSHostHeaderOverride == "" &&
		hostOverrides.HelmReValServiceHostHeaderOverride == "" &&
		(hostOverrides.NATSHostOverride == nil || *hostOverrides.NATSHostOverride == "") {
		return data, nil
	}

	var doc yamlv3.Node
	if err := yamlv3.Unmarshal(data, &doc); err != nil {
		return nil, err
	}

	root := &doc
	if doc.Kind == yamlv3.DocumentNode && len(doc.Content) > 0 {
		root = doc.Content[0]
	}
	if root.Kind != yamlv3.MappingNode {
		return nil, fmt.Errorf("agent config root is not a mapping")
	}

	agent := getYAMLMappingValue(root, "agent")
	if agent == nil {
		agent = &yamlv3.Node{Kind: yamlv3.MappingNode}
		root.Content = append(root.Content, yamlStringNode("agent"), agent)
	}
	if agent.Kind != yamlv3.MappingNode {
		return nil, fmt.Errorf("agent config agent section is not a mapping")
	}

	setYAMLString(agent, "icmsHostHeaderOverride", hostOverrides.ICMSHostHeaderOverride)
	setYAMLString(agent, "helmReValServiceHostHeaderOverride", hostOverrides.HelmReValServiceHostHeaderOverride)
	if hostOverrides.NATSHostOverride != nil {
		setYAMLString(agent, "NATSHostOverride", *hostOverrides.NATSHostOverride)
	}

	return yamlv3.Marshal(&doc)
}

func getYAMLMappingValue(node *yamlv3.Node, key string) *yamlv3.Node {
	if node == nil || node.Kind != yamlv3.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func setYAMLString(node *yamlv3.Node, key, value string) {
	if value == "" {
		return
	}
	valueNode := yamlStringNode(value)
	existing := getYAMLMappingValue(node, key)
	if existing != nil {
		*existing = *valueNode
		return
	}
	node.Content = append(node.Content, yamlStringNode(key), valueNode)
}

func yamlStringNode(value string) *yamlv3.Node {
	return &yamlv3.Node{
		Kind:  yamlv3.ScalarNode,
		Tag:   "!!str",
		Value: value,
	}
}

func (bc *BackendK8sCache) getAgentConfigToMerge(ctx context.Context) (nvcaconfig.Config, bool, error) {
	log := core.GetLogger(ctx)
	cm, err := bc.clients.K8s.CoreV1().ConfigMaps(bc.operatorNamespace).Get(ctx, agentConfigMergeConfigMapName, metav1.GetOptions{})
	if err != nil {
		if k8serr.IsNotFound(err) {
			return nvcaconfig.Config{}, false, nil
		}
		return nvcaconfig.Config{}, false, err
	}
	if len(cm.Data) == 0 || strings.TrimSpace(cm.Data[agentConfigFile]) == "" {
		log.Warn("Agent config merge ConfigMap exists but contains no data")
		return nvcaconfig.Config{}, false, nil
	}

	data := cm.Data[agentConfigFile]
	cfg, err := nvcaconfig.DecodeConfig([]byte(data))
	return cfg, true, err
}

func (bc *BackendK8sCache) getImageRegistryServerFromRepo(nb *nvidiaiov1.NVCFBackend) string {
	repo := nb.Spec.NVCAImageConfig.Repository
	if repo == "" {
		repo = bc.nvcaImageRepo
	}

	regSrv, _, found := strings.Cut(repo, "/")
	if !found {
		regSrv = repo
	}

	return regSrv
}

func (bc *BackendK8sCache) setupImagePullSecrets(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	log := core.GetLogger(ctx)

	// Skip setting up the image pull secret if generateImagePullSecret is disabled
	if !bc.generateImagePullSecret {
		log.Debugf("skip setting-up %v secret since generateImagePullSecret is disabled", NVCAImagePullSecretName)
		return nil
	}

	ngcServiceAPIStr, err := bc.ngcServiceKeyFetcher.FetchToken(ctx)
	if err != nil {
		log.WithError(err).Error("failed to fetch NGC Service API key")
		return fmt.Errorf("failed to fetch NGC Service API key, err: %v", err)
	}

	regSrv := bc.getImageRegistryServerFromRepo(nb)

	ipData, err := getImagePullSecretDockerConfigJSONFromNGCKey(regSrv, ngcServiceAPIStr)
	if err != nil {
		log.WithError(err).Error("failed to get secretdata for imagepull secrets")
		return fmt.Errorf("failed to get secretdata for imagepull secrets, err: %v", err)
	}

	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        NVCAImagePullSecretName,
			Namespace:   getSystemNamespace(nb),
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: ipData,
		},
	}

	return bc.createOrUpdateSecret(ctx, s)
}

func (bc *BackendK8sCache) getNVCAImagePathFromConfig(nb *nvidiaiov1.NVCFBackend) string {
	imgCfg := nb.Spec.NVCAImageConfig
	if imgCfg.Repository == "" {
		imgCfg.Repository = bc.nvcaImageRepo
	}
	if imgCfg.Tag == "" {
		imgCfg.Tag = nb.Spec.Version
	}
	return fmt.Sprintf("%s:%s", imgCfg.Repository, imgCfg.Tag)
}

func (bc *BackendK8sCache) getWebhooksImagePathFromConfig(ctx context.Context, nb *nvidiaiov1.NVCFBackend) string {
	imgCfg := nb.Spec.WebhookConfig.ImageConfig
	if imgCfg.Repository == "" {
		defaultRepoSuffix := nvcaoptypes.NVCAModuleName
		nvcaImageRepo := bc.nvcaImageRepo
		if nvcaImageRepo == "" {
			core.GetLogger(ctx).Error("code bug: nvcaImageRepo is empty, using default repo suffix nvca")
			nvcaImageRepo = nvcaoptypes.NVCAModuleName
		}
		lastSlashIdx := strings.LastIndex(nvcaImageRepo, "/")
		if lastSlashIdx < 0 {
			core.GetLogger(ctx).Info("code bug: nvcaImageRepo is a root repository, using default repo suffix nvca")
			imgCfg.Repository = fmt.Sprintf("%s/%s", nvcaImageRepo, defaultRepoSuffix)
			return fmt.Sprintf("%s:%s", imgCfg.Repository, imgCfg.Tag)
		}
		imgCfg.Repository = fmt.Sprintf("%s/%s", nvcaImageRepo[:lastSlashIdx], defaultRepoSuffix)
	}
	if imgCfg.Tag == "" {
		imgCfg.Tag = nb.Spec.Version
	}
	return fmt.Sprintf("%s:%s", imgCfg.Repository, imgCfg.Tag)
}

func getImagePullPolicyFromConfig(imgCfg nvidiaiov1.ImageConfig) corev1.PullPolicy {
	defPolicy := corev1.PullIfNotPresent
	if imgCfg.PullPolicy != "" {
		defPolicy = corev1.PullPolicy(imgCfg.PullPolicy)
	}
	return defPolicy
}

func getBackendType(nb *nvidiaiov1.NVCFBackend) string {
	backendType := DefaultBackendTypeK8s
	if nb.Spec.ClusterConfig.BackendType != "" {
		backendType = nb.Spec.ClusterConfig.BackendType
	}
	return backendType
}

func getClusterLogLevel(nb *nvidiaiov1.NVCFBackend) string {
	clusterLogLevel := DefaultLogLevel
	if nb.Spec.ClusterConfig.LogLevel != "" {
		clusterLogLevel = nb.Spec.ClusterConfig.LogLevel
	}
	return clusterLogLevel
}

func getNVCAListenPortHTTP(nb *nvidiaiov1.NVCFBackend) (int32, error) {
	return parseAddressPortOrDefault(nb.Spec.ClusterConfig.SvcAddress, DefaultNVCAListenPortHTTP)
}

func getNVCAAdminPortHTTP(nb *nvidiaiov1.NVCFBackend) (int32, error) {
	return parseAddressPortOrDefault(nb.Spec.ClusterConfig.AdminAddr, DefaultNVCAAdminPortHTTP)
}

func parseAddressPortOrDefault(addr string, defaultPort int32) (int32, error) {
	port := defaultPort
	if addr != "" {
		_, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return 0, fmt.Errorf("parse NVCA service address: %v", err)
		}
		port64, err := strconv.ParseInt(portStr, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("convert NVCA service port string: %v", err)
		}
		//nolint:gosec
		port = int32(port64)
	}
	return port, nil
}

const (
	nvcaHTTPPortName     = "http"
	webhookHTTPSPortName = "webhook-https"
)

func (bc *BackendK8sCache) setupNVCAService(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	nvcaListenPortHTTP, err := getNVCAListenPortHTTP(nb)
	if err != nil {
		return err
	}

	s := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        nvcaoptypes.NVCAModuleName,
			Namespace:   getSystemNamespace(nb),
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		Spec: corev1.ServiceSpec{
			Selector: getAppLabels(),
			Ports: []corev1.ServicePort{
				{
					Name:       nvcaoptypes.NVCAModuleName,
					Port:       nvcaListenPortHTTP,
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromString(nvcaHTTPPortName),
				},
				{
					Name:       "webhooks",
					Port:       getWebHooksSvcPort(nb),
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromString(webhookHTTPSPortName),
				},
			},
		},
	}

	return bc.createOrUpdateService(ctx, s)
}

func getDefaultContainerSecurityContext() *corev1.SecurityContext {
	// harden container security context
	privEsc := false
	nonRoot := true
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &privEsc,
		RunAsNonRoot:             &nonRoot,
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

func getImagePullSecretReferences(ctx context.Context, nb *nvidiaiov1.NVCFBackend, shouldGenerateImagePullSecret bool, additionalImagePullSecrets []corev1.LocalObjectReference) []corev1.LocalObjectReference {
	lor := []corev1.LocalObjectReference{}

	if shouldGenerateImagePullSecret {
		lor = append(lor, corev1.LocalObjectReference{Name: NVCAImagePullSecretName})

		if nb.Spec.NVCAImageConfig.PullSecretName != "" {
			lor = append(lor, corev1.LocalObjectReference{Name: nb.Spec.NVCAImageConfig.PullSecretName})
		}
	}

	// Add additional image pull secrets
	lor = append(lor, additionalImagePullSecrets...)

	if len(lor) == 0 {
		core.GetLogger(ctx).Debugf("no image pull secrets configured")
		return nil
	}

	return lor
}

// mergeAgentConfigs returns the effective agent config based on cluster source
// Exception: OverrideEnvironmentVars from operator takes precedence over NVCFBackend if non-nil
func (bc *BackendK8sCache) mergeAgentConfigs(nb *nvidiaiov1.NVCFBackend) nvidiaiov1.AgentConfig {
	// Capture operator override env vars before we use deploymentConfig
	operatorOverrideEnvVars := bc.deploymentConfig.OverrideEnvironmentVars

	// Build config with common local fields (used by both NGC and Helm)
	config := nvidiaiov1.AgentConfig{
		DeploymentConfig:                                       bc.deploymentConfig,
		AgentResources:                                         &corev1.ResourceRequirements{},
		WebhookResources:                                       &corev1.ResourceRequirements{},
		OTelCollectorResources:                                 &corev1.ResourceRequirements{},
		ByooResources:                                          &corev1.ResourceRequirements{},
		HelmReValStageOAuthTokenURL:                            nb.Spec.AgentConfig.HelmReValStageOAuthTokenURL,
		HelmReValStageOAuthPublicKeysetEndpoint:                nb.Spec.AgentConfig.HelmReValStageOAuthPublicKeysetEndpoint,
		HelmReValProdOAuthTokenURL:                             nb.Spec.AgentConfig.HelmReValProdOAuthTokenURL,
		HelmReValProdOAuthPublicKeysetEndpoint:                 nb.Spec.AgentConfig.HelmReValProdOAuthPublicKeysetEndpoint,
		FunctionDeploymentStagesStageOAuthTokenURL:             nb.Spec.AgentConfig.FunctionDeploymentStagesStageOAuthTokenURL,
		FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint: nb.Spec.AgentConfig.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint,
		FunctionDeploymentStagesProdOAuthTokenURL:              nb.Spec.AgentConfig.FunctionDeploymentStagesProdOAuthTokenURL,
		FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint:  nb.Spec.AgentConfig.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint,
	}

	bc.agentResources.DeepCopyInto(config.AgentResources)
	bc.webhookResources.DeepCopyInto(config.WebhookResources)
	bc.otelCollectorResources.DeepCopyInto(config.OTelCollectorResources)

	// OTel collector config: prefer NVCFBackend spec, fall back to bc.* (Helm env vars)
	config.OTelCollectorConfig = bc.getEffectiveOTelCollectorConfig(nb)

	// ByooResources comes from NVCFBackend spec (after overrides are merged)
	// This allows Helm values to override via nvcfBackend.overrides.agent.byooResources
	if nb.Spec.AgentConfig.ByooResources != nil {
		nb.Spec.AgentConfig.ByooResources.DeepCopyInto(config.ByooResources)
	}

	// Set NVCFWorkerConfig based on cluster source
	if nb.Spec.ClusterSource == nvcaoptypes.ClusterSourceNGCManaged {
		// NGC-managed: Use NGC-provided worker config
		config.NVCFWorkerConfig = nb.Spec.AgentConfig.NVCFWorkerConfig
	} else {
		// Helm-managed: Use local worker config
		config.NVCFWorkerConfig = bc.nvcfWorkerConfig
	}

	// If cache mount options are disabled, clear the options string
	if !config.NVCFWorkerConfig.CacheMountOptionsEnabled {
		config.NVCFWorkerConfig.CacheMountOptions = ""
	}

	// Special merge logic for OverrideEnvironmentVars:
	// - nil: operator didn't set anything (no env var), use NVCFBackend spec (fallback)
	// - non-nil (including empty map): operator explicitly set this, override NVCFBackend
	if operatorOverrideEnvVars != nil {
		config.DeploymentConfig.OverrideEnvironmentVars = operatorOverrideEnvVars
	} else {
		// Use NVCFBackend spec value if operator didn't set any
		config.DeploymentConfig.OverrideEnvironmentVars = nb.Spec.AgentConfig.DeploymentConfig.OverrideEnvironmentVars
	}
	// Tolerations: prefer the NVCFBackend CR (populated from cluster-dto.yaml in
	// helm-managed and self-managed modes) when non-empty; otherwise fall back to
	// the helm-supplied operator value. The fallback is required for ngc-managed
	// clusters because the NGC API does not currently surface agent tolerations,
	// so the CR's AgentConfig.Tolerations stays empty even when the user sets
	// agent.tolerations in helm values.
	if len(nb.Spec.AgentConfig.DeploymentConfig.Tolerations) > 0 {
		config.DeploymentConfig.Tolerations = append(
			[]corev1.Toleration(nil),
			nb.Spec.AgentConfig.DeploymentConfig.Tolerations...,
		)
	} else {
		config.DeploymentConfig.Tolerations = append(
			[]corev1.Toleration(nil),
			bc.deploymentConfig.Tolerations...,
		)
	}

	return config
}

func (bc *BackendK8sCache) getEffectiveAgentConfig(ctx context.Context, nb *nvidiaiov1.NVCFBackend) (nvidiaiov1.AgentConfig, error) {
	config := bc.mergeAgentConfigs(nb)

	var configMapGetter func(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.ConfigMap, error)
	if bc.clients != nil && bc.clients.K8s != nil {
		namespace := bc.operatorNamespace
		if namespace == "" {
			namespace = NVCAOperatorNamespace
		}
		configMapGetter = bc.clients.K8s.CoreV1().ConfigMaps(namespace).Get
	}

	chartDefaults, found, err := bc.getChartDefaultAgentConfig(ctx, configMapGetter)
	if err != nil {
		log := core.GetLogger(ctx)
		log.WithError(err).Warn("failed to read chart-default agent config, continuing without service OAuth defaults")
	} else if found {
		applyServiceOAuthDefaults(&config, chartDefaults)
	}

	return config, nil
}

func (bc *BackendK8sCache) getChartDefaultAgentConfig(
	ctx context.Context,
	configMapGetter func(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.ConfigMap, error),
) (nvidiaiov1.AgentConfig, bool, error) {
	if configMapGetter == nil {
		return nvidiaiov1.AgentConfig{}, false, nil
	}

	cm, err := configMapGetter(ctx, nvcfBackendChartDefaultsConfigMapName, metav1.GetOptions{})
	if k8serr.IsNotFound(err) {
		return nvidiaiov1.AgentConfig{}, false, nil
	}
	if err != nil {
		return nvidiaiov1.AgentConfig{}, false, fmt.Errorf("failed to read %s ConfigMap: %w", nvcfBackendChartDefaultsConfigMapName, err)
	}

	raw := strings.TrimSpace(cm.Data["cluster-dto.yaml"])
	if raw == "" {
		return nvidiaiov1.AgentConfig{}, false, nil
	}

	config, found, err := clustermgmt.AgentConfigFromClusterDTO(ctx, raw)
	if err != nil {
		return nvidiaiov1.AgentConfig{}, false, fmt.Errorf("failed to parse %s cluster-dto.yaml: %w", nvcfBackendChartDefaultsConfigMapName, err)
	}
	return config, found, nil
}

func applyServiceOAuthDefaults(config *nvidiaiov1.AgentConfig, defaults nvidiaiov1.AgentConfig) {
	if config.HelmReValStageOAuthTokenURL == "" {
		config.HelmReValStageOAuthTokenURL = defaults.HelmReValStageOAuthTokenURL
	}
	if config.HelmReValStageOAuthPublicKeysetEndpoint == "" {
		config.HelmReValStageOAuthPublicKeysetEndpoint = defaults.HelmReValStageOAuthPublicKeysetEndpoint
	}
	if config.HelmReValProdOAuthTokenURL == "" {
		config.HelmReValProdOAuthTokenURL = defaults.HelmReValProdOAuthTokenURL
	}
	if config.HelmReValProdOAuthPublicKeysetEndpoint == "" {
		config.HelmReValProdOAuthPublicKeysetEndpoint = defaults.HelmReValProdOAuthPublicKeysetEndpoint
	}
	if config.FunctionDeploymentStagesStageOAuthTokenURL == "" {
		config.FunctionDeploymentStagesStageOAuthTokenURL = defaults.FunctionDeploymentStagesStageOAuthTokenURL
	}
	if config.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint == "" {
		config.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint = defaults.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint
	}
	if config.FunctionDeploymentStagesProdOAuthTokenURL == "" {
		config.FunctionDeploymentStagesProdOAuthTokenURL = defaults.FunctionDeploymentStagesProdOAuthTokenURL
	}
	if config.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint == "" {
		config.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint = defaults.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint
	}
}

// getEffectiveK8sNetworkCIDRs returns the effective K8s network CIDRs to use.
// It prefers values from the NVCFBackend spec (fetched from NGC API) over static Helm values.
// This ensures that network policy changes from NGC are properly detected and trigger rollouts.
func (bc *BackendK8sCache) getEffectiveK8sNetworkCIDRs(nb *nvidiaiov1.NVCFBackend) []string {
	// Use spec values from NGC if available (these are dynamically fetched)
	if len(nb.Spec.ClusterConfig.K8sClusterNetworkCIDRs) > 0 {
		return nb.Spec.ClusterConfig.K8sClusterNetworkCIDRs
	}
	// Fall back to Helm-configured values if spec is empty
	return bc.k8sClusterNetworkCIDRs
}

func (bc *BackendK8sCache) setupNVCADeployment(ctx context.Context, original *nvidiaiov1.NVCFBackend) error {
	// TODO: Remove bart-system namespace at the end of the Setup
	log := core.GetLogger(ctx)
	deployName := nvcaoptypes.NVCAModuleName

	log.Infof("Creating deployment %v", deployName)

	// Get the NGC Service API key
	_, err := bc.ngcServiceKeyFetcher.FetchToken(ctx)
	if err != nil {
		log.WithError(err).Error("failed to fetch NGC Service API key")
		return err
	}

	// Get effective config by merging ICMS and local configs
	nb := original.DeepCopy()
	agentConfig, err := bc.getEffectiveAgentConfig(ctx, original)
	if err != nil {
		return err
	}
	nb.Spec.AgentConfig = agentConfig

	volumes := []corev1.Volume{{
		Name: agentConfigVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: agentConfigConfigMapName,
				},
				DefaultMode: ptr.To(int32(0644)),
				Optional:    ptr.To(false),
			},
		},
	}}

	// defaults
	nvcaListenPortHTTP, err := getNVCAListenPortHTTP(nb)
	if err != nil {
		return err
	}

	nvcaContainer := corev1.Container{
		Name:            "agent",
		Image:           bc.getNVCAImagePathFromConfig(nb),
		ImagePullPolicy: getImagePullPolicyFromConfig(nb.Spec.NVCAImageConfig),
		Args:            []string{"/usr/bin/nvca", "--config", agentConfigFilePath},
		SecurityContext: getDefaultContainerSecurityContext(),
		Resources:       bc.agentResources,
		Ports: []corev1.ContainerPort{
			{
				Name:          nvcaHTTPPortName,
				ContainerPort: nvcaListenPortHTTP,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      NGCServiceAPIKeySecretName,
				MountPath: fmt.Sprintf("/var/run/secrets/%s", NGCServiceAPIKeySecretName),
				ReadOnly:  true,
			},
			{
				Name:      agentConfigVolumeName,
				MountPath: agentConfigDir,
			},
		},
	}

	// pod Security params choose one that works for anything
	runAsUser := bc.nvcaRunAsUserID
	runAsGroup := bc.nvcaRunAsGroupID

	volumes = append(volumes,
		corev1.Volume{
			Name: SrvCertsVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		corev1.Volume{
			Name: CACertsVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		corev1.Volume{
			Name: NGCServiceAPIKeySecretName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: NGCServiceAPIKeySecretName,
				},
			},
		},
	)

	msCfg := nb.Spec.ClusterConfig.MiniService.Complete(bc.envType)
	volumes = append(volumes, corev1.Volume{
		Name: ReValCacheVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{
				SizeLimit: msCfg.CacheDirSize,
			},
		},
	})
	nvcaContainer.VolumeMounts = append(nvcaContainer.VolumeMounts, corev1.VolumeMount{
		Name:      ReValCacheVolumeName,
		MountPath: ReValCacheDir,
	})

	// Add OAuth authentication environment variables.
	oauthConfig := getOAuthConfig(nb)
	oauthEnvVars := getOAuthEnvVars(nb, oauthConfig)
	nvcaContainer.Env = append(nvcaContainer.Env, oauthEnvVars...)

	// Apply custom env overrides from spec.agentConfig.overrideEnvironmentVars (last so they take precedence)
	if overrides := nb.Spec.AgentConfig.DeploymentConfig.OverrideEnvironmentVars; len(overrides) > 0 {
		nvcaContainer.Env = append(nvcaContainer.Env, envVarsFromMap(overrides)...)
	}

	if nb.Spec.FeatureGate.OTELConfig != nil {
		nvcaContainer.EnvFrom = append(nvcaContainer.EnvFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: OTELConfigSecretName,
				},
			},
		})
	}

	if nb.Spec.VaultConfig.Enabled {
		expiration := int64(3600)

		vaultSrvAddress := DefaultVaultServerAddress
		if nb.Spec.VaultConfig.Address != "" {
			vaultSrvAddress = nb.Spec.VaultConfig.Address
		}

		volumes = append(volumes, corev1.Volume{
			Name: "token",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{
							ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
								Path:              "token",
								ExpirationSeconds: &expiration,
								Audience:          vaultSrvAddress,
							},
						},
					},
				},
			},
		})

		nvcaContainer.VolumeMounts = append(nvcaContainer.VolumeMounts, corev1.VolumeMount{
			Name:      "token",
			MountPath: "/var/run/secrets/kubernetes.io/serviceaccount-vault",
		})
	}

	// Probe setup.

	// The startup probe should return fairly quickly since the /version endpoint
	// is static and is initialized early in NVCA's startup process.
	// Overall allowed time before restart is 30 seconds.
	nvcaContainer.StartupProbe = &corev1.Probe{
		FailureThreshold:    30,
		InitialDelaySeconds: 1,
		PeriodSeconds:       1,
		SuccessThreshold:    1,
		TimeoutSeconds:      5,
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   "/version",
				Port:   intstr.FromInt(int(nvcaListenPortHTTP)),
				Scheme: corev1.URISchemeHTTP,
			},
		},
	}
	// The liveness probe may take awhile to return HTTP 200 as most initialization logic
	// occurs before all checkers are added to the handler.
	// The high failure threshold and initial delay should account for startup latency
	// even on a busy cluster.
	// Overall allowed time before restart is 150 seconds.
	nvcaContainer.LivenessProbe = &corev1.Probe{
		FailureThreshold:    30,
		InitialDelaySeconds: 5,
		PeriodSeconds:       5,
		SuccessThreshold:    1,
		TimeoutSeconds:      5,
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   "/livez",
				Port:   intstr.FromInt(int(nvcaListenPortHTTP)),
				Scheme: corev1.URISchemeHTTP,
			},
		},
	}
	// The readiness probe may take awhile to return HTTP 200 as most initialization logic
	// occurs before all checkers are added to the handler.
	nvcaContainer.ReadinessProbe = &corev1.Probe{
		FailureThreshold:    3,
		InitialDelaySeconds: 5,
		PeriodSeconds:       5,
		SuccessThreshold:    1,
		TimeoutSeconds:      5,
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   "/healthz",
				Port:   intstr.FromInt(int(nvcaListenPortHTTP)),
				Scheme: corev1.URISchemeHTTP,
			},
		},
	}

	webhooksListenPort := getWebHooksListenPort(nb)

	webhooksContainer := corev1.Container{
		Name:            NVCAWebhookContainerName,
		Image:           bc.getWebhooksImagePathFromConfig(ctx, nb),
		ImagePullPolicy: getImagePullPolicyFromConfig(nb.Spec.WebhookConfig.ImageConfig),
		Args:            []string{"/usr/bin/webhook-server", "--config", agentConfigFilePath},
		SecurityContext: getDefaultContainerSecurityContext(),
		Env: []corev1.EnvVar{
			{
				Name: "POD_NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.namespace",
					},
				},
			},
		},
		Resources: bc.webhookResources,
		Ports: []corev1.ContainerPort{
			{
				Name:          webhookHTTPSPortName,
				ContainerPort: webhooksListenPort,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      SrvCertsVolumeName,
				MountPath: SrvCertsMountDir,
			},
			{
				Name:      CACertsVolumeName,
				MountPath: CACertsMountDir,
			},
			{
				Name:      agentConfigVolumeName,
				MountPath: agentConfigDir,
			},
		},
	}

	volumes = append(volumes, bc.getOTelCollectorVolume(nb)...)
	containers := []corev1.Container{nvcaContainer, webhooksContainer}
	initContainers := append([]corev1.Container{}, bc.getOTelCollectorContainer(nb)...)

	replicas := int32(1)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        deployName,
			Namespace:   getSystemNamespace(nb),
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: getAppLabels(),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: getAppLabels(),
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: boolPtr(true),
					HostNetwork:                  nb.Spec.ICMSConfig.IsLocal,
					ServiceAccountName:           nvcaoptypes.NVCAModuleName,
					InitContainers:               initContainers,
					Containers:                   containers,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:  &runAsUser,
						RunAsGroup: &runAsGroup,
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Volumes: volumes,
				},
			},
		},
	}

	if nb.Spec.VaultConfig.Enabled {
		deployment.Spec.Template.Annotations = mergeMaps(deployment.Spec.Template.Annotations, getVaultAnnotations(nb))
	}

	// apply agent deployment customizations
	agentCfg := nb.Spec.AgentConfig

	// node selectors
	if agentCfg.DeploymentConfig.NodeSelectorKey != "" && agentCfg.DeploymentConfig.NodeSelectorValue != "" {
		deployment.Spec.Template.Spec.NodeSelector = map[string]string{
			agentCfg.DeploymentConfig.NodeSelectorKey: agentCfg.DeploymentConfig.NodeSelectorValue,
		}
	}

	// priority class
	if agentCfg.DeploymentConfig.PriorityClassName != "" {
		deployment.Spec.Template.Spec.PriorityClassName = agentCfg.DeploymentConfig.PriorityClassName
	}
	if len(agentCfg.DeploymentConfig.Tolerations) > 0 {
		deployment.Spec.Template.Spec.Tolerations = append(
			[]corev1.Toleration(nil),
			agentCfg.DeploymentConfig.Tolerations...,
		)
	}

	// if self managed, apply self managed deployment
	if IsSelfHosted(nb) {
		nvcaImage := bc.getNVCAImagePathFromConfig(nb)
		if err := applySelfManagedNVCADeployment(ctx, nb, deployment, bc.clients.K8s, bc.identitySource, nvcaImage); err != nil {
			return err
		}
	}

	if err := bc.createOrUpdateDeployment(ctx, deployment); err != nil {
		return fmt.Errorf("failed setupNVCADeployment, err: %v", err)
	}
	return nil
}

func mergeMaps(m1 map[string]string, maps ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, m := range append([]map[string]string{m1}, maps...) {
		for k, v := range m {
			merged[k] = v
		}
	}
	return merged
}

//go:embed manifests/*
var manifests embed.FS

func (bc *BackendK8sCache) getNetworkPoliciesData(ctx context.Context, nb *nvidiaiov1.NVCFBackend) (map[string]string, error) {
	log := core.GetLogger(ctx)
	type templateInputBase struct {
		AppName                string
		InstanceName           string
		ManagedBy              string
		DDCSIPAllowList        []string
		K8sClusterNetworkCIDRs []string
	}

	// Use effective network CIDRs from spec (NGC) if available, otherwise fall back to Helm values
	effectiveK8sNetworkCIDRs := bc.getEffectiveK8sNetworkCIDRs(nb)

	tiBase := templateInputBase{
		AppName:                nvcaoptypes.NVCAModuleName,
		InstanceName:           nvcaoptypes.NVCAModuleName,
		ManagedBy:              NVCAOperatorName,
		DDCSIPAllowList:        bc.ddcsIPAllowList,
		K8sClusterNetworkCIDRs: effectiveK8sNetworkCIDRs,
	}
	t, err := template.ParseFS(manifests, filepath.Join("manifests", "netpol", "*"))
	if err != nil {
		return nil, fmt.Errorf("parse netpol manifests: %w", err)
	}

	netpols := map[string]string{}
	b := &bytes.Buffer{}
	for _, tt := range t.Templates() {
		var data any
		npName := strings.TrimSuffix(filepath.Base(tt.Name()), filepath.Ext(tt.Name()))

		switch npName {
		case EgressGXCacheNetworkPolicyNameKey:
			data = struct {
				templateInputBase
				GXCacheNamespace string
			}{
				templateInputBase: tiBase,
				GXCacheNamespace:  bc.gxCacheNamespace,
			}
		case EgressCrowdstrikeAllowPolicyNameKey, IngressCrowdstrikeNetworkPolicyNameKey:
			data = struct {
				templateInputBase
				CrowdstrikeNamespace string
			}{
				templateInputBase:    tiBase,
				CrowdstrikeNamespace: bc.crowdstrikeNamespace,
			}
		default:
			data = tiBase
		}

		if err := tt.Execute(b, data); err != nil {
			log.WithError(err).Errorf("Failed to execute template for policy %s", npName)
			return nil, err
		}

		netpols[npName] = b.String()

		b.Reset()
	}

	return netpols, nil
}

func sanitizeStringWithoutExtraSpaces(input, separator string) string {
	// sanitize if passed with extra spaces
	inputParts := strings.Split(input, separator)

	// Trim whitespace from each part and store in a new slice
	var trimmedInputParts []string
	for _, part := range inputParts {
		if part == "" {
			continue
		}
		trimmedInputParts = append(trimmedInputParts, strings.TrimSpace(part))
	}
	// Join the trimmed parts back together with the original separator
	return strings.Join(trimmedInputParts, separator)
}

func (bc *BackendK8sCache) newAgentConfig(ctx context.Context, nb *nvidiaiov1.NVCFBackend) (cfg nvcaconfig.Config, err error) {
	environment := nvcaconfig.EnvironmentProduction
	if bc.envType == nvidiaiov1.EnvTypeStage {
		environment = nvcaconfig.EnvironmentStaging
	}

	logLevel := getClusterLogLevel(nb)
	if _, err := logrus.ParseLevel(logLevel); err != nil {
		return cfg, err
	}

	requestsNamespace := getRequestsNamespace(nb)
	systemNamespace := getSystemNamespace(nb)
	backendType := getBackendType(nb)

	if strings.EqualFold(backendType, BCPACEBackendType) {
		requestsNamespace = systemNamespace
	}

	nvcaAdminPortHTTP, err := getNVCAAdminPortHTTP(nb)
	if err != nil {
		return cfg, err
	}
	nvcaListenPortHTTP, err := getNVCAListenPortHTTP(nb)
	if err != nil {
		return cfg, err
	}

	tokenURL := nb.Spec.ICMSConfig.TokenURL
	if tokenURL == "" {
		oauthConfig := getOAuthConfig(nb)
		if oauthConfig.TokenURL != "" {
			tokenURL = oauthConfig.TokenURL
		}
		// Note: getOAuthConfig() only returns config when OAuthConfig has a client ID.
	}

	effectiveConfig, err := bc.getEffectiveAgentConfig(ctx, nb)
	if err != nil {
		return cfg, err
	}

	var featureFlags []string
	if ffs := bc.getNVCAFeatureFlags(nb); ffs != "" {
		featureFlags = strings.Split(ffs, ",")
	}

	cfg = nvcaconfig.Config{
		Environment: environment,
		Cluster: nvcaconfig.NVCFClusterConfig{
			NCAID:         nb.Spec.AccountConfig.NCAID,
			ID:            nb.Spec.ClusterConfig.ClusterID,
			Name:          nb.Spec.ClusterConfig.ClusterName,
			GroupID:       nb.Spec.ClusterConfig.ClusterGroupID,
			GroupName:     nb.Spec.ClusterConfig.ClusterGroupName,
			Region:        nb.Spec.ClusterConfig.Region,
			Attributes:    nb.Spec.ClusterConfig.Attributes,
			CloudProvider: nb.Spec.ClusterConfig.CloudProvider,
		},
		Agent: nvcaconfig.AgentConfig{
			LogLevel:                                logLevel,
			FeatureFlags:                            featureFlags,
			ICMSURL:                                 nb.Spec.ICMSConfig.ICMSServiceURL,
			SvcAddress:                              fmt.Sprintf(":%d", nvcaListenPortHTTP),
			AdminAddr:                               fmt.Sprintf("127.0.0.1:%d", nvcaAdminPortHTTP),
			SystemNamespace:                         systemNamespace,
			RequestsNamespace:                       requestsNamespace,
			NamespaceLabels:                         getAppLabels(),
			ComputeBackend:                          backendType,
			HelmRepositoryPrefix:                    bc.helmRepositoryPrefix,
			HelmReValStageOAuthTokenURL:             effectiveConfig.HelmReValStageOAuthTokenURL,
			HelmReValStageOAuthPublicKeysetEndpoint: effectiveConfig.HelmReValStageOAuthPublicKeysetEndpoint,
			HelmReValProdOAuthTokenURL:              effectiveConfig.HelmReValProdOAuthTokenURL,
			HelmReValProdOAuthPublicKeysetEndpoint:  effectiveConfig.HelmReValProdOAuthPublicKeysetEndpoint,
			FunctionDeploymentStagesStageOAuthTokenURL:             effectiveConfig.FunctionDeploymentStagesStageOAuthTokenURL,
			FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint: effectiveConfig.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint,
			FunctionDeploymentStagesProdOAuthTokenURL:              effectiveConfig.FunctionDeploymentStagesProdOAuthTokenURL,
			FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint:  effectiveConfig.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint,
			OperatorVersion:             bc.nvcaOperatorVersion,
			KubernetesVersionOverride:   bc.k8sVersionOverride,
			ImageCredentialHelperImage:  nb.Spec.ImageCredHelper.Complete(bc.envType).ImageConfig.BuildImageRef(),
			SecretMirrorSourceNamespace: effectiveConfig.DeploymentConfig.SecretMirrorSourceNamespace,
			SecretMirrorLabelSelector:   effectiveConfig.DeploymentConfig.SecretMirrorLabelSelector,
			Tolerations:                 append([]corev1.Toleration(nil), effectiveConfig.DeploymentConfig.Tolerations...),
		},
		Webhook: nvcaconfig.WebhookConfig{
			SvcAddress:    fmt.Sprintf(":%d", getWebHooksListenPort(nb)),
			TLSCertFile:   fmt.Sprintf("%s/%s", SrvCertsMountDir, TLSCertName),
			TLSKeyFile:    fmt.Sprintf("%s/%s", SrvCertsMountDir, TLSKeyName),
			TLSSecretName: NVCAWebhookTLSCertSecretName,
		},
		Workload: nvcaconfig.WorkloadConfig{
			Tolerations: append([]corev1.Toleration(nil), bc.workloadTolerations...),
		},
		Authz: nvcaconfig.AuthzConfig{
			TokenURL:             tokenURL,
			PublicKeysetEndpoint: getOAuthConfig(nb).PublicKeysetEndpoint,
			NGCServiceAPIKeyFile: fmt.Sprintf("/var/run/secrets/%s/%s", NGCServiceAPIKeySecretName, NGCServiceAPIKeySecretDataKey),
		},
		Tracing: nvcaconfig.TracingConfig{},
	}

	// Apply worker config
	if effectiveConfig.NVCFWorkerConfig.WorkerDegradationPeriod != 0 {
		cfg.Workload.WorkerDegradationTimeout = effectiveConfig.NVCFWorkerConfig.WorkerDegradationPeriod
	}

	if effectiveConfig.NVCFWorkerConfig.CacheMountOptionsEnabled {
		if effectiveConfig.NVCFWorkerConfig.CacheMountOptions == "" {
			log.Panic("Cache volume mount options enabled, but no options passed")
		}
		sanitizedMountOptions := sanitizeStringWithoutExtraSpaces(effectiveConfig.NVCFWorkerConfig.CacheMountOptions, ",")
		if sanitizedMountOptions != "" {
			cfg.Agent.CSIVolumeMountOptions = strings.Split(sanitizedMountOptions, ",")
		}
	}

	if nb.Spec.VaultConfig.Enabled {
		vaultSecretFilePath := DefaultVaultSecretFilePath
		if nb.Spec.VaultConfig.SecretFilePath != "" {
			vaultSecretFilePath = nb.Spec.VaultConfig.SecretFilePath
		}
		// Use the OAuth client secrets file rendered by the Vault agent.
		cfg.Authz.ClientSecretsEnvFile = getClientSecretsEnvFile(vaultSecretFilePath, nb.Spec.Version)
	}

	// TODO: add SPIRE once it is supported in NVCA
	switch bc.identitySource {
	case IdentitySourcePSAT:
		cfg.Authz.ClusterIssuedTokenSource = nvcaconfig.ClusterIssuedTokenSourcePSAT
		cfg.Authz.ClusterIssuedTokenFilePath = clusterIssuedTokenFilePath
	case "":
		// No identity source set, no need to set paths.
	default:
		return cfg, fmt.Errorf("unsupported identity source: %s", bc.identitySource)
	}

	// Add environment variable overrides for function and task workloads to agent config
	if bc.functionEnvOverridesB64 != "" {
		envOverrides, err := decodeEnvOverrides(bc.functionEnvOverridesB64)
		if err != nil {
			return cfg, fmt.Errorf("decode function env overrides: %w", err)
		}
		cfg.Workload.FunctionEnvOverrides = envOverrides
	}
	if bc.taskEnvOverridesB64 != "" {
		envOverrides, err := decodeEnvOverrides(bc.taskEnvOverridesB64)
		if err != nil {
			return cfg, fmt.Errorf("decode task env overrides: %w", err)
		}
		cfg.Workload.TaskEnvOverrides = envOverrides
	}

	// Pass byooResources to NVCA for BYOO otel collector container in function pods
	if effectiveConfig.ByooResources != nil {
		cfg.Agent.BYOOResources = nvcaconfig.ResourceRequirements{
			Limits:   nvcaconfig.ResourceList(effectiveConfig.ByooResources.Limits),
			Requests: nvcaconfig.ResourceList(effectiveConfig.ByooResources.Requests),
			Claims:   effectiveConfig.ByooResources.Claims,
		}
	}

	if c := nb.Spec.ClusterConfig.GPUDiscovery.Static; c != nil {
		cfg.Agent.StaticGPUCapacity = c.AllocatedGPUCapacity
	}

	cfg.Agent.HelmReValServiceURL = nb.Spec.ClusterConfig.MiniService.Complete(bc.envType).HelmReValServiceURL

	// In self-hosted mode, default to the colocated ReVal service unless explicitly configured in the DTO.
	if usesSelfHostedColocatedReVal(nb) {
		cfg.Agent.HelmReValServiceURL = "http://reval.nvcf.svc.cluster.local:8080"
	}

	if nb.Spec.ClusterConfig.FNDService.IsEnabled(nb.Spec.FeatureGate.Values) {
		c := nb.Spec.ClusterConfig.FNDService.Complete(bc.envType, nb.Spec.FeatureGate.Values)
		cfg.Agent.FunctionDeploymentStagesServiceURL = c.ServiceURL
	}

	// Shared storage config
	const (
		smbServerImageProd  = "nvcr.io/qtfpt1h0bieu/nvcf-core/samba:1.0.5"
		smbServerImageStage = "stg.nvcr.io/nv-cf/nvcf-core/samba:1.0.5"
	)
	var smbServerImage string
	if nb.Spec.SharedStorageImage != nil {
		if imageRef := nb.Spec.SharedStorageImage.BuildImageRef(); imageRef != "" {
			smbServerImage = imageRef
		}
	}
	if smbServerImage == "" {
		if cfg.Environment == nvcaconfig.EnvironmentStaging {
			smbServerImage = smbServerImageStage
		} else {
			smbServerImage = smbServerImageProd
		}
	}
	cfg.Agent.SharedStorage = nvcaconfig.SharedStorageConfig{
		Server: nvcaconfig.SharedStorageServerConfig{
			Image: smbServerImage,
		},
	}
	if c := nb.Spec.FeatureGate.SharedStorage; c != nil && c.Server.SMBServerContainerResources != nil {
		cfg.Agent.SharedStorage.Server.ContainerResources = nvcaconfig.ResourceRequirements{
			Limits:   nvcaconfig.ResourceList(c.Server.SMBServerContainerResources.Limits),
			Requests: nvcaconfig.ResourceList(c.Server.SMBServerContainerResources.Requests),
			Claims:   c.Server.SMBServerContainerResources.Claims,
		}
	}

	ipsCfg, err := completeInternalPersistentStorageConfig(ctx, nb)
	if err != nil {
		return cfg, err
	}
	if ipsCfg != nil && ipsCfg.Enabled {
		cfg.Agent.InternalPersistentStorage = nvcaconfig.InternalPersistentStorageConfig{
			StorageClassName:  ipsCfg.StorageClassName,
			HardResourceQuota: nvcaconfig.ResourceList(ipsCfg.ResourceQuota.Hard),
		}
	}

	if c := nb.Spec.FeatureGate.OTELConfig; c != nil {
		cfg.Tracing.LightstepServiceName = c.ServiceName
		// Endpoint and access token are set in a secret and sourced by environment variable.
		if nb.Spec.FeatureGate.OTELConfig.Exporter != "" {
			cfg.Tracing.Exporter = nvcaconfig.OTELExporter(nb.Spec.FeatureGate.OTELConfig.Exporter)
		} else {
			cfg.Tracing.Exporter = DefaultOTELExporter
		}
	}

	return cfg, nil
}

func usesSelfHostedColocatedReVal(nb *nvidiaiov1.NVCFBackend) bool {
	return IsSelfHosted(nb) && (nb.Spec.ClusterConfig.MiniService == nil || nb.Spec.ClusterConfig.MiniService.HelmReValServiceURL == "")
}

func (bc *BackendK8sCache) setupNVCAMutatingWebhookConfiguration(ctx context.Context,
	nb *nvidiaiov1.NVCFBackend,
	webhookCert WebhookCert,
) error {
	whc := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:        nvcaoptypes.NVCAModuleName,
			Annotations: getNBAnnotations(nb),
		},
	}

	whc.Webhooks = append(whc.Webhooks, bc.makeMiniServiceMutatingWebhooks(nb, webhookCert)...)
	whc.Webhooks = append(whc.Webhooks, bc.makeNVCAPodNodeAffinityMutatingWebhook(nb, webhookCert))
	whc.Webhooks = append(whc.Webhooks, bc.makePodEnforcementMutatingWebhooks(nb, webhookCert)...)
	whc.Webhooks = append(whc.Webhooks,
		// Note: the helm storage mutating webhook is now just a stub for backwards-compatibility.
		// The MiniService mutating webhook now handles all storage mutations.
		// This must be removed in a future release.
		makeHelmStorageMutatingWebhook(nb, webhookCert),
		bc.makeHelmPersistentStorageWebhook(nb, webhookCert),
		makeNVCAMutatingWebhook(nb, webhookCert))

	return bc.createOrUpdateMutatingWebhookConfiguration(ctx, whc)
}

// returns a defaulted internal persistent storage configuration if enabled
func completeInternalPersistentStorageConfig(ctx context.Context, nb *nvidiaiov1.NVCFBackend) (*nvidiaiov1.InternalPersistentStorageSpec, error) {
	if nb.Spec.FeatureGate.InternalPersistentStorage == nil {
		return &nvidiaiov1.InternalPersistentStorageSpec{}, nil
	}
	if !nb.Spec.FeatureGate.InternalPersistentStorage.Enabled {
		return nb.Spec.FeatureGate.InternalPersistentStorage, nil
	}

	log := core.GetLogger(ctx)
	// if empty storage class name log a warning and return nothing
	if nb.Spec.FeatureGate.InternalPersistentStorage.StorageClassName == "" {
		log.Error("Storage class name must be specified to enable internal-persistent-storage")
		return nil, fmt.Errorf("storage class name is missing from internal persistent storage config")
	}

	// Otherwise build the configuration from the storage class
	dto := nb.Spec.FeatureGate.InternalPersistentStorage.DeepCopy()
	if dto.ResourceQuota.Hard == nil {
		dto.ResourceQuota.Hard = corev1.ResourceList{}
	}

	// Ensure there is a setting for requests.storage
	if _, ok := dto.ResourceQuota.Hard[corev1.ResourceRequestsStorage]; !ok {
		dto.ResourceQuota.Hard[corev1.ResourceRequestsStorage] = resource.MustParse("500Gi")
	}
	return dto, nil
}

// returns a string of the internal persisten storage configuration base64 encoded
func getInternalPersistentStorageConfig(ctx context.Context, nb *nvidiaiov1.NVCFBackend) (string, error) {
	log := core.GetLogger(ctx)
	dto, err := completeInternalPersistentStorageConfig(ctx, nb)
	if err != nil {
		return "", err
	}
	if dto == nil || !dto.Enabled {
		return "", nil
	}

	buff := &bytes.Buffer{}
	if err := json.NewEncoder(buff).Encode(dto); err != nil {
		log.WithError(err).Error("failed to encode the persistent storage configuration")
		return "", err
	}

	return base64.StdEncoding.EncodeToString(buff.Bytes()), nil
}

func getSharedStorageConfig(ctx context.Context, nb *nvidiaiov1.NVCFBackend) (string, error) {
	// if empty return empty string
	if nb.Spec.FeatureGate.SharedStorage == nil ||
		!slices.Contains(nb.Spec.FeatureGate.Values, "HelmSharedStorage") {
		return "", nil
	}

	log := core.GetLogger(ctx)
	buff := &bytes.Buffer{}
	err := json.NewEncoder(buff).Encode(nb.Spec.FeatureGate.SharedStorage)
	if err != nil {
		log.WithError(err).Error("failed to encode the persistent storage configuration")
		return "", err
	}

	return base64.StdEncoding.EncodeToString(buff.Bytes()), nil
}

// =============================================================================
// OTel Collector Helper Functions
// =============================================================================

func (bc *BackendK8sCache) isOTelCollectorEnabled(nb *nvidiaiov1.NVCFBackend) bool {
	return bc.getEffectiveOTelCollectorConfig(nb).Enabled
}

// getEffectiveOTelCollectorConfig returns the resolved OTel collector config,
// preferring NVCFBackend spec values and falling back to bc.* (Helm env vars).
func (bc *BackendK8sCache) getEffectiveOTelCollectorConfig(nb *nvidiaiov1.NVCFBackend) *nvidiaiov1.OTelCollectorConfig {
	enabled := bc.otelCollectorEnabled
	repo := bc.otelCollectorImageRepo
	tag := bc.otelCollectorImageTag

	if nb.Spec.OTelCollector != nil {
		enabled = nb.Spec.OTelCollector.Enabled
		if nb.Spec.OTelCollector.ImageConfig.Repository != "" {
			repo = nb.Spec.OTelCollector.ImageConfig.Repository
		}
		if nb.Spec.OTelCollector.ImageConfig.Tag != "" {
			tag = nb.Spec.OTelCollector.ImageConfig.Tag
		}
	}

	return &nvidiaiov1.OTelCollectorConfig{
		Enabled: enabled,
		ImageConfig: nvidiaiov1.ImageConfig{
			Repository: repo,
			Tag:        tag,
		},
	}
}

func (bc *BackendK8sCache) setupOTelCollectorConfigMap(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	log := core.GetLogger(ctx)
	if !bc.isOTelCollectorEnabled(nb) {
		// Remove the ConfigMap when OTel collector is disabled to clean orphaned state. Log error instead of blocking.
		if err := bc.deleteConfigMapIfExists(ctx, getSystemNamespace(nb), NVCAOTelCollectorConfigMapName); err != nil {
			log.WithError(err).Warn("failed to delete OTel collector ConfigMap when disabled; continuing")
		}
		return nil
	}

	log.Info("setting up OTel collector ConfigMap")

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        NVCAOTelCollectorConfigMapName,
			Namespace:   getSystemNamespace(nb),
			Annotations: getNBAnnotations(nb),
			Labels:      getAppLabels(),
		},
		Data: bc.getOTelCollectorConfigData(),
	}

	return bc.createOrUpdateConfigMap(ctx, cm)
}

func (bc *BackendK8sCache) getOTelCollectorImagePath(nb *nvidiaiov1.NVCFBackend) string {
	cfg := bc.getEffectiveOTelCollectorConfig(nb)
	return fmt.Sprintf("%s:%s", cfg.ImageConfig.Repository, cfg.ImageConfig.Tag)
}

// getOTelCollectorContainerCommandArgsAndEnv returns command, args, and env vars for the OTel Collector container.
func (bc *BackendK8sCache) getOTelCollectorContainerCommandArgsAndEnv(nb *nvidiaiov1.NVCFBackend) ([]string, []string, []corev1.EnvVar) {
	command := []string{"/otelcol-contrib"}
	args := []string{
		fmt.Sprintf("--config=%s/config.yaml", NVCAOTelCollectorConfigMountPath),
	}

	fndsEndpoint := getFNDSEndpoint(nb.Spec.ClusterConfig.FNDService, bc.envType)
	authCfg := bc.getOTelCollectorAuthConfig(nb)

	env := []corev1.EnvVar{
		{
			Name:  NGCServiceAPIKeyFileEnvVar,
			Value: fmt.Sprintf("/var/run/secrets/%s/%s", NGCServiceAPIKeySecretName, NGCServiceAPIKeySecretDataKey),
		},
		{
			Name:  NVCAOTelCollectorRequestsNamespaceEnvVar,
			Value: getRequestsNamespace(nb),
		},
		{
			Name:  NVCAOTelCollectorFNDSEndpointEnvVar,
			Value: fmt.Sprintf("%s/v3/ledger/k8s-events", fndsEndpoint),
		},
		{
			Name:  NVCAOTelCollectorHealthCheckPortEnvVar,
			Value: fmt.Sprintf("%d", NVCAOTelCollectorHealthCheckPort),
		},
		{
			Name:  NVCAOTelCollectorMetricsPortEnvVar,
			Value: fmt.Sprintf("%d", NVCAOTelCollectorMetricsPort),
		},
		{
			Name:  NVCAOTelCollectorMemoryLimitPercentageEnvVar,
			Value: fmt.Sprintf("%d", NVCAOTelCollectorMemoryLimitPercentage),
		},
		{
			Name:  NVCAOTelCollectorSpikeLimitPercentageEnvVar,
			Value: fmt.Sprintf("%d", NVCAOTelCollectorSpikeLimitPercentage),
		},
		{
			Name:  NVCAOTelCollectorOAuthClientIDEnvVar,
			Value: authCfg.clientID,
		},
		{
			Name:  NVCAOTelCollectorOAuthClientSecretFileEnvVar,
			Value: authCfg.clientSecretFile,
		},
		{
			Name:  NVCAOTelCollectorOAuthTokenURLEnvVar,
			Value: authCfg.tokenURL,
		},
		{
			Name:  NVCAOTelCollectorAuthenticatorEnvVar,
			Value: authCfg.authenticator,
		},
	}
	return command, args, env
}

// getOTelCollectorVolumeMounts returns volume mounts for the OTel collector container.
func (bc *BackendK8sCache) getOTelCollectorVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      NVCAOTelCollectorConfigMapName,
			MountPath: NVCAOTelCollectorConfigMountPath,
			ReadOnly:  true,
		},
		{
			Name:      NGCServiceAPIKeySecretName,
			MountPath: fmt.Sprintf("/var/run/secrets/%s", NGCServiceAPIKeySecretName),
			ReadOnly:  true,
		},
	}
}

// getOTelCollectorContainer returns the OTel collector as a restartable init container.
// Using restartPolicy: Always (Kubernetes 1.28+) allows the init container to run continuously
func (bc *BackendK8sCache) getOTelCollectorContainer(nb *nvidiaiov1.NVCFBackend) []corev1.Container {
	if !bc.isOTelCollectorEnabled(nb) {
		return nil
	}
	command, args, env := bc.getOTelCollectorContainerCommandArgsAndEnv(nb)
	restartPolicyAlways := corev1.ContainerRestartPolicyAlways
	return []corev1.Container{
		{
			Name:            NVCAOTelCollectorContainerName,
			Image:           bc.getOTelCollectorImagePath(nb),
			ImagePullPolicy: getImagePullPolicyFromConfig(nb.Spec.OTelCollector.Complete(bc.envType).ImageConfig),
			Command:         command,
			Args:            args,
			Env:             env,
			RestartPolicy:   &restartPolicyAlways,
			SecurityContext: getDefaultContainerSecurityContext(),
			Resources:       bc.otelCollectorResources,
			Ports: []corev1.ContainerPort{
				{
					Name:          NVCAOTelCollectorHealthCheckPortName,
					ContainerPort: NVCAOTelCollectorHealthCheckPort,
					Protocol:      corev1.ProtocolTCP,
				},
				{
					Name:          NVCAOTelCollectorMetricsPortName,
					ContainerPort: NVCAOTelCollectorMetricsPort,
					Protocol:      corev1.ProtocolTCP,
				},
			},
			VolumeMounts: bc.getOTelCollectorVolumeMounts(),
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/",
						Port: intstr.FromInt(int(NVCAOTelCollectorHealthCheckPort)),
					},
				},
				InitialDelaySeconds: 5,
				PeriodSeconds:       10,
				TimeoutSeconds:      5,
				FailureThreshold:    3,
			},
		},
	}
}

func (bc *BackendK8sCache) getOTelCollectorVolume(nb *nvidiaiov1.NVCFBackend) []corev1.Volume {
	if !bc.isOTelCollectorEnabled(nb) {
		return nil
	}
	return []corev1.Volume{
		{
			Name: NVCAOTelCollectorConfigMapName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: NVCAOTelCollectorConfigMapName,
					},
				},
			},
		},
	}
}
