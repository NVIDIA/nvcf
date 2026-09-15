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

package v1

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
)

type EnvType string

const (
	EnvTypeStage EnvType = "stage"
	EnvTypeProd  EnvType = "prod"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type AgentStatus string

// +k8s:openapi-gen=true
const (
	AgentStatusUnknown   AgentStatus = "Unknown"
	AgentStatusHealthy   AgentStatus = "Healthy"
	AgentStatusUnhealthy AgentStatus = "Unhealthy"
)

// +k8s:openapi-gen=true
type ImageConfig struct {
	Repository     string `json:"repository"`
	Tag            string `json:"tag"`
	PullPolicy     string `json:"pullPolicy"`
	PullSecretName string `json:"pullSecretName"`
}

func (cfg ImageConfig) BuildImageRef() string {
	if cfg.Repository == "" || cfg.Tag == "" {
		return ""
	}
	if strings.HasPrefix(cfg.Tag, "sha256:") {
		return fmt.Sprintf("%s@%s", cfg.Repository, cfg.Tag)
	}
	return fmt.Sprintf("%s:%s", cfg.Repository, cfg.Tag)
}

// +k8s:openapi-gen=true
type AccountConfig struct {
	NCAID string `json:"ncaID"`
}

// +k8s:openapi-gen=true
type ClusterConfig struct {
	// required
	ClusterID        string             `json:"clusterID,omitempty"`
	ClusterName      string             `json:"clusterName"`
	ClusterGroupName string             `json:"clusterGroupName"`
	ClusterGroupID   string             `json:"clusterGroupID,omitempty"`
	CloudProvider    string             `json:"cloudProvider"`
	GPUDiscovery     GPUDiscoveryConfig `json:"gpuDiscovery"`
	MiniService      *MiniServiceConfig `json:"miniService,omitempty"`
	FNDService       *FNDServiceConfig  `json:"fndService,omitempty"`
	Region           string             `json:"region"`

	// optional fields
	Description            string   `json:"description,omitempty"`
	LogLevel               string   `json:"logLevel,omitempty"`
	RequestsNamespace      string   `json:"requestsNamespace,omitempty"`
	UnregisterOnStartup    bool     `json:"unregisterOnStartUp,omitempty"`
	SvcAddress             string   `json:"serviceAddr,omitempty"`
	AdminAddr              string   `json:"adminAddr,omitempty"`
	SystemNamespace        string   `json:"systemNamespace,omitempty"`
	BackendType            string   `json:"backendType,omitempty"`
	Attributes             []string `json:"attributes,omitempty"`
	K8sClusterNetworkCIDRs []string `json:"k8sClusterNetworkCIDRs,omitempty"`

	// NvSnap configures the per-cluster behavior of the NvSnap
	// checkpoint/restore integration. Empty (or unset Enabled flag)
	// → integration disabled on this cluster; pod creation behaves
	// identically to today. The global featureflag.NvSnapCheckpointRestore
	// short-circuits this field — both must be true for either hook
	// to fire. See docs/users/nvsnap/NVSNAP-INTEGRATION-DESIGN.md.
	NvSnap *NvSnapConfig `json:"nvsnap,omitempty"`
}

// +k8s:openapi-gen=true
type ICMSConfig struct {
	ICMSServiceURL                string `json:"icmsServiceURL"`
	ICMSServiceHostHeaderOverride string `json:"icmsServiceHostHeaderOverride,omitempty"`
	AWSEndpointURL                string `json:"awsEndpointURL,omitempty"`
	IsLocal                       bool   `json:"isLocal,omitempty"`
	TokenURL                      string `json:"tokenURL"`
}

// +k8s:openapi-gen=true
type OTELConfig struct {
	Exporter    string `json:"exporter"`
	Endpoint    string `json:"endpoint,omitempty"`
	ServiceName string `json:"serviceName"`
	AccessToken string `json:"accessToken"`
}

// +k8s:openapi-gen=true
type VaultConfig struct {
	Enabled        bool   `json:"enabled"`
	Address        string `json:"address"`
	VaultNamespace string `json:"vaultNamespace,omitempty"`
	SecretFilePath string `json:"secretFilePath,omitempty"`
	// Should have the format: auth/jwt/k8s/<cluster-name>
	AuthMountPath string `json:"authMountPath,omitempty"`
	// Should have the format: k8s_<cluster-name>_bart_jwt_role
	OAuthConfigRole string `json:"oauthConfigRole,omitempty"`
	// OAuthClientMountPath is the vault mount path for OAuth2/OIDC client secrets.
	// Should have the format: nvidia/services/oauth/clients/<client-id>/kv/secret
	OAuthClientMountPath string `json:"oauthClientMountPath,omitempty"`
	// The secret should be raw data if auto-rotation is set up, not "key=value".
	// default to .Data.data.secret
	SecretDataPath string `json:"secretDataPath,omitempty"`
}

func legacyOAuthConfigJSONKey() string {
	return "s" + "saConfig"
}

// +k8s:openapi-gen=true
// OAuthConfig provides OAuth2/OIDC authentication configuration.
// This is the preferred configuration method.
type OAuthConfig struct {
	TokenURL             string `json:"tokenURL"`
	ClientID             string `json:"clientID"`
	PublicKeysetEndpoint string `json:"publicKeysetEndpoint"` // JWKS endpoint for token validation
	ClientSecretKey      string `json:"clientSecretKey,omitempty"`
	ClientSecretsEnvFile string `json:"clientSecretsEnvFile"`
	TokenScope           string `json:"tokenScope,omitempty"`
}

// +k8s:openapi-gen=true
type WebhookConfig struct {
	ListenPort  int32       `json:"listenPort,omitempty"`
	ServicePort int32       `json:"servicePort,omitempty"`
	ImageConfig ImageConfig `json:"imageConfig,omitempty"`
}

// +k8s:openapi-gen=true
type ImageCredHelperConfig struct {
	ImageConfig ImageConfig `json:"imageConfig,omitempty"`
}

func (cfg *ImageCredHelperConfig) Complete(_ EnvType) *ImageCredHelperConfig {
	var tmp *ImageCredHelperConfig
	if cfg == nil {
		tmp = &ImageCredHelperConfig{}
	} else {
		tmp = cfg.DeepCopy()
	}
	return tmp
}

// +k8s:openapi-gen=true
type OTelCollectorConfig struct {
	Enabled     bool        `json:"enabled,omitempty"`
	ImageConfig ImageConfig `json:"imageConfig,omitempty"`
}

func (cfg *OTelCollectorConfig) IsEnabled() bool {
	return cfg != nil && cfg.Enabled
}

func (cfg *OTelCollectorConfig) Complete(_ EnvType) *OTelCollectorConfig {
	var tmp *OTelCollectorConfig
	if cfg == nil {
		tmp = &OTelCollectorConfig{}
	} else {
		tmp = cfg.DeepCopy()
	}
	return tmp
}

var (
	// 50Gi default reval cache dir size.
	defaultReValCacheDirSize = resource.NewQuantity(50*1<<30, resource.BinarySI)
)

const (
	helmReValServiceURLStg  = "https://reval.stg.nvcf.nvidia.com"
	helmReValServiceURLProd = "https://reval.nvcf.nvidia.com"
)

// +k8s:openapi-gen=true
type MiniServiceConfig struct {
	HelmReValServiceURL                string             `json:"helmReValServiceURL"`
	HelmReValServiceHostHeaderOverride string             `json:"helmReValServiceHostHeaderOverride,omitempty"`
	CacheDirSize                       *resource.Quantity `json:"cacheDirSize"`
}

const (
	// DefaultNvSnapServerURL is the in-cluster nvsnap-server Service URL.
	// Operators can override per cluster for off-cluster deployments
	// (e.g., a hosted nvsnap-server fronting a fleet of NVCA clusters).
	DefaultNvSnapServerURL = "http://nvsnap-server.nvsnap-system.svc.cluster.local:8080"

	// DefaultNvSnapWarmupTimeoutSeconds caps how long NVCA polls
	// /v1/health/ready on the inference container before giving up on
	// a checkpoint attempt. 15 min covers TRT-LLM 70B engine compile.
	DefaultNvSnapWarmupTimeoutSeconds = 15 * 60

	// DefaultNvSnapWarmupBufferSeconds is the gap between the first
	// /health 200 and the actual checkpoint POST. Flipped from 10 → 0
	// in the v0.0.49 / rootfs-everywhere design: an honest readiness
	// probe is the contract; padding every capture by 10s adds latency
	// for no benefit and can capture mid-flight CUDA-graph builds that
	// destabilize the snapshot. Operators that need a workload-specific
	// dwell can still set spec.nvsnap.warmupBufferSeconds explicitly
	// per-NVCFBackend.
	DefaultNvSnapWarmupBufferSeconds = 0
)

// NvSnapConfig is the per-cluster configuration for the NvSnap
// checkpoint/restore integration. All fields default to "off" or to
// the in-cluster Service URL — a default-constructed NvSnapConfig is
// a no-op.
//
// Layered disable order (any false short-circuits):
//
//	featureflag.NvSnapCheckpointRestore  (global kill switch)
//	→ NvSnapConfig.IntegrationEnabled    (per-cluster)
//	→ NvSnapFunctionState.spec.optOut    (per-function-version)
//
// IntegrationEnabled controls the checkpoint side (Hook B).
// RestoreEnabled controls the restore side (Hook A) — split so we
// can roll out checkpoint-only first, then restore once we've
// gathered timing data.
//
// +k8s:openapi-gen=true
type NvSnapConfig struct {
	// IntegrationEnabled turns on the post-Ready checkpoint reconciler
	// (Hook B). When false, no pod is checkpointed on this cluster.
	IntegrationEnabled bool `json:"integrationEnabled,omitempty"`

	// RestoreEnabled turns on the restore-on-create hook (Hook A).
	// When false, NVCA does not stamp nvsnap.io/restore-from on new
	// pods even if a checkpoint hash exists in NGC. Lets us validate
	// checkpoint capture independently of restore.
	RestoreEnabled bool `json:"restoreEnabled,omitempty"`

	// ServerURL is the nvsnap-server endpoint NVCA's checkpoint reconciler
	// POSTs to. Empty → DefaultNvSnapServerURL (in-cluster Service).
	ServerURL string `json:"serverURL,omitempty"`

	// WarmupTimeoutSeconds caps the per-pod health-check polling
	// window before NVCA gives up on triggering a checkpoint. 0 →
	// DefaultNvSnapWarmupTimeoutSeconds (15 min).
	WarmupTimeoutSeconds int32 `json:"warmupTimeoutSeconds,omitempty"`

	// WarmupBufferSeconds is the dwell time between first /health 200
	// and the checkpoint POST. Default 0 (no extra dwell — the
	// readiness probe is the contract). Set explicitly to override
	// per-NVCFBackend for workloads that need post-Ready dwell time
	// (e.g., lazy-JIT engines that finish compiling shortly after the
	// first 200). The previous semantics ("0 means use 10s default")
	// conflated "I want zero" with "I didn't set this", so this field
	// no longer treats 0 as a sentinel; the value you set is the
	// value used.
	WarmupBufferSeconds int32 `json:"warmupBufferSeconds,omitempty"`
}

// Complete returns a copy of NvSnapConfig with empty fields filled from
// the defaults. Returns a non-nil pointer even when called on nil,
// so callers can rely on the result for field reads without nil
// checks. The returned value has the same enable bits as the input
// (defaults do not opt anything in).
func (c *NvSnapConfig) Complete(_ EnvType) *NvSnapConfig {
	var tmp *NvSnapConfig
	if c == nil {
		tmp = &NvSnapConfig{}
	} else {
		tmp = c.DeepCopy()
	}
	if tmp.ServerURL == "" {
		tmp.ServerURL = DefaultNvSnapServerURL
	}
	if tmp.WarmupTimeoutSeconds == 0 {
		tmp.WarmupTimeoutSeconds = DefaultNvSnapWarmupTimeoutSeconds
	}
	// WarmupBufferSeconds: NO fallback. The value you set is the
	// value used. Default 0 (DefaultNvSnapWarmupBufferSeconds) is a
	// real choice, not a sentinel — see the field's doc comment.
	return tmp
}

// IsEnabled reports whether the per-cluster integration is on AT ALL
// — either checkpoint-side, restore-side, or both. The global feature
// flag (featureflag.NvSnapCheckpointRestore) is a separate AND-gate
// checked by callers.
func (c *NvSnapConfig) IsEnabled() bool {
	return c != nil && (c.IntegrationEnabled || c.RestoreEnabled)
}

func (msCfg *MiniServiceConfig) Complete(envType EnvType) *MiniServiceConfig {
	var tmp *MiniServiceConfig
	if msCfg == nil {
		tmp = &MiniServiceConfig{}
	} else {
		tmp = msCfg.DeepCopy()
	}
	if tmp.HelmReValServiceURL == "" {
		switch envType {
		case EnvTypeStage:
			tmp.HelmReValServiceURL = helmReValServiceURLStg
		default:
			tmp.HelmReValServiceURL = helmReValServiceURLProd
		}
	}
	if tmp.CacheDirSize == nil {
		tmp.CacheDirSize = defaultReValCacheDirSize
	}
	return tmp
}

const (
	FunctionDeploymentStagesServiceURLStg  = "https://deployment-stages.stg.nvcf.nvidia.com"
	FunctionDeploymentStagesServiceURLProd = "https://deployment-stages.nvcf.nvidia.com"
)

// +k8s:openapi-gen=true
type FNDServiceConfig struct {
	Enabled    *bool  `json:"enabled"`
	ServiceURL string `json:"serviceURL"`
}

func (fndsCfg *FNDServiceConfig) IsEnabled(featureFlags []string) bool {
	return (fndsCfg != nil && fndsCfg.Enabled != nil && *fndsCfg.Enabled) ||
		slices.Contains(featureFlags, featureflag.UseFunctionDeploymentStages.Key)
}

func (fndsCfg *FNDServiceConfig) Complete(envType EnvType, featureFlags []string) *FNDServiceConfig {
	if !fndsCfg.IsEnabled(featureFlags) {
		return fndsCfg
	}

	serviceURL := ""
	if fndsCfg != nil {
		serviceURL = fndsCfg.ServiceURL
	}

	tmp := &FNDServiceConfig{
		Enabled:    &[]bool{true}[0],
		ServiceURL: serviceURL,
	}
	if tmp.ServiceURL == "" {
		switch envType {
		case EnvTypeStage:
			tmp.ServiceURL = FunctionDeploymentStagesServiceURLStg
		default:
			tmp.ServiceURL = FunctionDeploymentStagesServiceURLProd
		}
	}
	return tmp
}

// +k8s:openapi-gen=true
type FeatureGate struct {
	SharedStorage             *SharedStorageSpec             `json:"sharedStorage,omitempty"`
	InternalPersistentStorage *InternalPersistentStorageSpec `json:"internalPersistentStorage,omitempty"`
	OTELConfig                *OTELConfig                    `json:"otelConfig,omitempty"`
	Values                    []string                       `json:"values,omitempty"`
}

// +k8s:openapi-gen=true
type SharedStorageSpec struct {
	Server *SharedStorageServerSpec `json:"server,omitempty"`
}

// +k8s:openapi-gen=true
type SharedStorageServerSpec struct {
	SMBServerContainerResources *corev1.ResourceRequirements `json:"smbServerContainerResources,omitempty"`
}

// +k8s:openapi-gen=true
type InternalPersistentStorageSpec struct {
	Enabled          bool                                       `json:"enabled"`
	StorageClassName string                                     `json:"storageClassName"`
	ResourceQuota    InternalPersistentStorageResourceQuotaSpec `json:"resourceQuota"`
}

// +k8s:openapi-gen=true
type InternalPersistentStorageResourceQuotaSpec struct {
	// hard is the set of desired hard limits for each named resource.
	// More info: https://kubernetes.io/docs/concepts/policy/resource-quotas/
	// +optional
	Hard corev1.ResourceList `json:"hard,omitempty" protobuf:"bytes,1,rep,name=hard,casttype=ResourceList,castkey=ResourceName"`
}

// +k8s:openapi-gen=true
type GPUDiscoveryConfig struct {
	// Dynamic GPU discovery is enabled by default.
	Dynamic *DynamicGPUDiscoveryConfig `json:"dynamic,omitempty"`
	Static  *StaticGPUDiscoveryConfig  `json:"static,omitempty"`
}

type DynamicGPUDiscoveryConfig struct{}

type StaticGPUDiscoveryConfig struct {
	AllocatedGPUCapacity uint64 `json:"allocatedGPUCapacity"`
	ConfigMapName        string `json:"configMapName"`
	// StaticGPUConfig is used internally to store a GPU config
	// from the cluster management API payload.
	GPUConfig string `json:"gpuConfig"`
}

// NVCFBackendSpec defines the desired state of NVCFBackend
// +k8s:openapi-gen=true
type NVCFBackendSpec struct {
	NVCFBackendSpecT

	Overrides *NVCFBackendSpecT `json:"overrides,omitempty"`
}

// UnmarshalJSON implements custom deserialization for NVCFBackendSpec.
// The embedded NVCFBackendSpecT has its own UnmarshalJSON which consumes the
// full JSON data, preventing Go from populating the Overrides field. This
// method explicitly handles both the inline fields and the Overrides pointer.
func (spec *NVCFBackendSpec) UnmarshalJSON(data []byte) error {
	// Unmarshal the inline NVCFBackendSpecT fields.
	if err := json.Unmarshal(data, &spec.NVCFBackendSpecT); err != nil {
		return err
	}

	// Unmarshal the Overrides field separately since the inline struct's
	// custom UnmarshalJSON prevents Go from handling it automatically.
	var raw struct {
		Overrides *NVCFBackendSpecT `json:"overrides,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	spec.Overrides = raw.Overrides
	return nil
}

// +k8s:openapi-gen=true
type DeploymentConfig struct {
	// PriorityClassName for pod preference during evictions
	PriorityClassName string `json:"priorityClassName,omitempty"`
	// NodeSelector key for pod scheduling
	NodeSelectorKey string `json:"nodeSelectorKey,omitempty"`
	// NodeSelector value for pod scheduling
	NodeSelectorValue string `json:"nodeSelectorValue,omitempty"`
	// SecretMirrorSourceNamespace is the namespace to source secrets from for mirroring
	SecretMirrorSourceNamespace string `json:"secretMirrorSourceNamespace,omitempty"`
	// SecretMirrorLabelSelector is the label selector to filter which secrets to mirror
	SecretMirrorLabelSelector string `json:"secretMirrorLabelSelector,omitempty"`
	// GenerateImagePullSecret is passed in from the operator to the agent, if true,
	// the agent will generate an image pull secret for nvca Pods
	// using the NGC service key.
	GenerateImagePullSecret bool `json:"generateImagePullSecret,omitempty"`
	// AdditionalImagePullSecrets is the list of additional image pull secrets currently configured
	AdditionalImagePullSecrets []corev1.LocalObjectReference `json:"additionalImagePullSecrets,omitempty"`
	// OverrideEnvironmentVars is a map of environment variables to override on the NVCA agent container.
	// These take precedence over default values computed by the operator.
	// Example: {"LOG_LEVEL": "debug", "CUSTOM_FLAG": "enabled"}
	OverrideEnvironmentVars map[string]string `json:"overrideEnvironmentVars,omitempty"`
	// Tolerations configures tolerations for the NVCA agent pod.
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

type NVCFBackendSpecT struct {
	Version         string        `json:"version,omitempty"`
	NVCAImageConfig ImageConfig   `json:"nvcaImageConfig,omitempty"`
	AccountConfig   AccountConfig `json:"accountConfig"`
	ClusterConfig   ClusterConfig `json:"clusterConfig"`
	FeatureGate     FeatureGate   `json:"featureGate,omitempty"`
	// OAuthConfig provides OAuth2/OIDC authentication configuration (preferred).
	OAuthConfig   OAuthConfig   `json:"oauthConfig,omitempty"`
	ICMSConfig    ICMSConfig    `json:"icmsConfig"`
	VaultConfig   VaultConfig   `json:"vaultConfig,omitempty"`
	WebhookConfig WebhookConfig `json:"webhookConfig,omitempty"`
	AgentConfig   AgentConfig   `json:"agentConfig,omitempty"`
	// ImageCredHelper configures the "nvcf-image-credential-helper" image in NVCA.
	ImageCredHelper *ImageCredHelperConfig `json:"imageCredentialHelper,omitempty"`
	// SharedStorageImage configures the shared storage image in NVCA.
	SharedStorageImage *ImageConfig `json:"sharedStorageImage,omitempty"`
	// OTelCollector configures the OpenTelemetry Collector sidecar image in NVCA.
	OTelCollector *OTelCollectorConfig `json:"otelCollector,omitempty"`
	// ClusterSource is the source of the cluster configuration.
	ClusterSource ClusterSource `json:"clusterSource,omitempty"`
}

func (spec *NVCFBackendSpecT) UnmarshalJSON(data []byte) error {
	type nvcfBackendSpecTAlias NVCFBackendSpecT

	var alias nvcfBackendSpecTAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if alias.OAuthConfig.ClientID == "" {
		if value, ok := raw[legacyOAuthConfigJSONKey()]; ok {
			if err := json.Unmarshal(value, &alias.OAuthConfig); err != nil {
				return err
			}
		}
	}

	*spec = NVCFBackendSpecT(alias)
	return nil
}

// ClusterSource defines the source of cluster configuration.
type ClusterSource string

const (
	ClusterSourceNGCManaged  ClusterSource = "ngc-managed"
	ClusterSourceHelmManaged ClusterSource = "helm-managed"
	ClusterSourceSelfHosted  ClusterSource = "self-hosted"
	ClusterSourceSelfManaged ClusterSource = ClusterSourceSelfHosted
)

func (cs ClusterSource) String() string {
	return string(cs)
}

func (cs ClusterSource) IsValid() bool {
	switch cs {
	case ClusterSourceNGCManaged, ClusterSourceHelmManaged, ClusterSourceSelfHosted:
		return true
	default:
		return false
	}
}

type NVCFWorkerConfig struct {
	CacheMountOptionsEnabled bool          `json:"cacheMountOptionsEnabled"`
	CacheMountOptions        string        `json:"cacheMountOptions,omitempty"`
	WorkerDegradationPeriod  time.Duration `json:"workerDegradationPeriod,omitempty"`
}

type AgentConfig struct {
	AgentResources                                         *corev1.ResourceRequirements `json:"agentResources,omitempty"`
	WebhookResources                                       *corev1.ResourceRequirements `json:"webhookResources,omitempty"`
	OTelCollectorResources                                 *corev1.ResourceRequirements `json:"otelCollectorResources,omitempty"`
	ByooResources                                          *corev1.ResourceRequirements `json:"byooResources,omitempty"`
	OTelCollectorConfig                                    *OTelCollectorConfig         `json:"otelCollectorConfig,omitempty"`
	NATSURL                                                *string                      `json:"natsURL,omitempty"`
	NATSHostOverride                                       *string                      `json:"natsHostOverride,omitempty"`
	LLMRequestRouterAddress                                string                       `json:"llmRequestRouterAddress,omitempty"`
	HelmReValStageOAuthTokenURL                            string                       `json:"helmReValStageOAuthTokenURL,omitempty"`
	HelmReValStageOAuthPublicKeysetEndpoint                string                       `json:"helmReValStageOAuthPublicKeysetEndpoint,omitempty"`
	HelmReValProdOAuthTokenURL                             string                       `json:"helmReValProdOAuthTokenURL,omitempty"`
	HelmReValProdOAuthPublicKeysetEndpoint                 string                       `json:"helmReValProdOAuthPublicKeysetEndpoint,omitempty"`
	FunctionDeploymentStagesStageOAuthTokenURL             string                       `json:"functionDeploymentStagesStageOAuthTokenURL,omitempty"`
	FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint string                       `json:"functionDeploymentStagesStageOAuthPublicKeysetEndpoint,omitempty"` //nolint:lll
	FunctionDeploymentStagesProdOAuthTokenURL              string                       `json:"functionDeploymentStagesProdOAuthTokenURL,omitempty"`
	FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint  string                       `json:"functionDeploymentStagesProdOAuthPublicKeysetEndpoint,omitempty"` //nolint:lll
	DeploymentConfig
	NVCFWorkerConfig
}

// NVCFBackendStatus defines the observed state of NVCFBackend
type NVCFBackendStatus struct {
	NVCFBackendSpecT

	AgentStatus            AgentStatus             `json:"agentStatus,omitempty"`
	GPUUsage               map[GPUName]GPUResource `json:"gpuUsage,omitempty"`
	KubernetesVersion      string                  `json:"kubernetesVersion,omitempty"`
	LastUpdated            *metav1.Time            `json:"lastUpdated"`
	LastUpdatedAgentStatus *metav1.Time            `json:"lastUpdatedAgentStatus"`

	// Network configuration tracking for rollout detection
	DDCSIPAllowList        []string `json:"ddcsIPAllowList,omitempty"`
	K8sClusterNetworkCIDRs []string `json:"k8sClusterNetworkCIDRs,omitempty"`

	// Environment variable overrides for workloads (base64-encoded JSON maps)
	FunctionEnvOverridesB64 string `json:"functionEnvOverridesB64,omitempty"`
	TaskEnvOverridesB64     string `json:"taskEnvOverridesB64,omitempty"`

	AgentConfig
}

func (status *NVCFBackendStatus) UnmarshalJSON(data []byte) error {
	var spec NVCFBackendSpecT
	if err := json.Unmarshal(data, &spec); err != nil {
		return err
	}

	var statusOnly struct {
		AgentStatus             AgentStatus             `json:"agentStatus,omitempty"`
		GPUUsage                map[GPUName]GPUResource `json:"gpuUsage,omitempty"`
		KubernetesVersion       string                  `json:"kubernetesVersion,omitempty"`
		LastUpdated             *metav1.Time            `json:"lastUpdated"`
		LastUpdatedAgentStatus  *metav1.Time            `json:"lastUpdatedAgentStatus"`
		DDCSIPAllowList         []string                `json:"ddcsIPAllowList,omitempty"`
		K8sClusterNetworkCIDRs  []string                `json:"k8sClusterNetworkCIDRs,omitempty"`
		FunctionEnvOverridesB64 string                  `json:"functionEnvOverridesB64,omitempty"`
		TaskEnvOverridesB64     string                  `json:"taskEnvOverridesB64,omitempty"`
		AgentConfig
	}
	if err := json.Unmarshal(data, &statusOnly); err != nil {
		return err
	}

	*status = NVCFBackendStatus{
		NVCFBackendSpecT:        spec,
		AgentStatus:             statusOnly.AgentStatus,
		GPUUsage:                statusOnly.GPUUsage,
		KubernetesVersion:       statusOnly.KubernetesVersion,
		LastUpdated:             statusOnly.LastUpdated,
		LastUpdatedAgentStatus:  statusOnly.LastUpdatedAgentStatus,
		DDCSIPAllowList:         statusOnly.DDCSIPAllowList,
		K8sClusterNetworkCIDRs:  statusOnly.K8sClusterNetworkCIDRs,
		FunctionEnvOverridesB64: statusOnly.FunctionEnvOverridesB64,
		TaskEnvOverridesB64:     statusOnly.TaskEnvOverridesB64,
		AgentConfig:             statusOnly.AgentConfig,
	}
	return nil
}

// +genclient
// +k8s:openapi-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// NVCFBackend is the Schema for the nvcfbackends API
type NVCFBackend struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NVCFBackendSpec   `json:"spec,omitempty"`
	Status NVCFBackendStatus `json:"status,omitempty"`
}

// +k8s:openapi-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// no client needed for list as it's been created in above
// NVCFBackendList contains a list of NVCFBackend
type NVCFBackendList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NVCFBackend `json:"items"`
}

// GPUName is the normalized name of the GPU
type GPUName string

// GPUResource lists the amount of the particular gpu
type GPUResource struct {
	Capacity  uint64 `json:"capacity,omitempty"`
	Available uint64 `json:"available,omitempty"`
	Allocated uint64 `json:"allocated,omitempty"`
}
