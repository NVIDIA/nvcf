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
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/scheme"
	nvcainformers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/informers/externalversions"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/cleanup"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/metrics"
	nvcaoptel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/otel"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/reconcile/clustermgmt"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

type mockTokenFetcher struct {
	token string
	err   error
}

func (m *mockTokenFetcher) FetchToken(ctx context.Context) (string, error) {
	return m.token, m.err
}

// getEphemeralPort finds an available ephemeral port on localhost
func getEphemeralPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// TestAgent just invokes the underlying APIs as a sanity
// that just invocation itself doesn't panic
// we can improve more complex UTs as follow-on
func TestAgent(t *testing.T) {
	SyncNVCFBackendInterval = 10 * time.Millisecond
	ctx := core.WithDefaultLogger(context.Background())

	expectedNCAID := "some-nca-id"
	expectedClusterName := "mars"
	expectedVersion := "1.0.0"
	defaultLabelValues := []string{expectedNCAID, expectedClusterName, expectedVersion}

	// Use unique metrics prefix to avoid collisions across test runs
	metricsPrefix := fmt.Sprintf("nvca_test_%d", time.Now().UnixNano())
	ctx = metrics.WithDefaultMetrics(ctx, metricsPrefix, defaultLabelValues)
	m := metrics.FromContext(ctx)
	t.Cleanup(func() {
		if m != nil {
			m.Destroy()
		}
	})

	fakeExit := func(int) {}

	p := PatchOSExit(t, fakeExit)
	t.Cleanup(p.Unpatch)

	// Create mock NGC API server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mock response for GET /v2/icms/clusters/{clusterID}
		if r.Method == http.MethodGet && r.URL.Path == "/v2/icms/clusters/random-clusterid" {
			mockCluster := map[string]interface{}{
				"clusterId":          "random-clusterid",
				"clusterName":        expectedClusterName,
				"clusterDescription": "Test cluster",
				"clusterGroupName":   "test-group",
				"clusterGroupId":     "test-group-id",
				"status":             "active",
				"ncaID":              expectedNCAID,
				"nvcaVersion":        expectedVersion,
				"oAuthClientId":      "test-client-id",
				"cloudProvider":      "test-cloud",
				"region":             "test-region",
				"attributes":         []string{},
				"capabilities":       []string{},
				"gpus":               []interface{}{},
				"icmsConfig": map[string]string{
					"publicKeysetEndpoint": "https://test.endpoint/keys",
					"tokenURL":             "https://test.endpoint/token",
					"icmsServiceURL":       "https://test.endpoint/icms",
				},
				"vaultConfig": map[string]string{
					"address": "https://test.vault.endpoint",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(mockCluster)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(mockServer.Close)

	// Create a temporary directory for kubeconfig
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig.yaml")

	// Get ephemeral ports to avoid conflicts
	svcPort := getEphemeralPort(t)
	adminPort := getEphemeralPort(t)

	a := AgentOptions{
		KubeConfigPath:                kubeconfigPath,
		SystemNamespace:               NVCAOperatorNamespace,
		SvcAddress:                    fmt.Sprintf("localhost:%d", svcPort),
		AdminAddr:                     fmt.Sprintf("localhost:%d", adminPort),
		K8sVersionOverride:            "1.25.8",
		TokenFetcher:                  &mockTokenFetcher{token: "randomkey"},
		NVCFClusterID:                 "random-clusterid",
		NVCAClusterAPIRefreshInterval: 1 * time.Millisecond,
		NVCAClusterManagementAPIURL:   mockServer.URL,
		ClusterSource:                 nvcaoptypes.ClusterSourceNGCManaged, // Set the cluster source
		// Add AgentConfig related fields
		PriorityClassName:               "high-priority",
		NodeSelectorKey:                 "node-type",
		NodeSelectorValue:               "nvca",
		NVCASecretMirrorSourceNamespace: "source-ns",
		NVCASecretMirrorLabelSelector:   "mirror=true",
		NVCACacheMountOptionsEnabled:    true,
		NVCACacheMountOptions:           "ro,noatime",
		NVCAWorkerDegradationPeriod:     90 * time.Minute,
		NVCAWorkloadTolerations: []corev1.Toleration{{
			Key:      "nvidia.com/test-workload",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		}},
		AgentResources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
		WebhookResources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("250m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
		OTelCollectorImageRepo: "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib",
		OTelCollectorImageTag:  "0.139.0",
		OTelCollectorResources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1000m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
	}
	ag, err := NewAgent(ctx, &a)
	assert.Nil(t, err)

	// Create the kubeconfig file
	mockKubeConfigFile(t, kubeconfigPath)

	// Mock the kube clients creation
	ag.getBackendK8sKubeClientsChFunc = func(ctx context.Context) <-chan *core.KubeClients {
		ch := make(chan *core.KubeClients, 1)
		ch <- &core.KubeClients{
			Config: &rest.Config{},
			K8s:    mockKubeClients().K8s,
		}
		close(ch)
		return ch
	}

	// Create a mock builder that will be used by the agent
	clients := mockKubeClients()

	// Create informer factories
	k8sFactory := k8sinformers.NewSharedInformerFactoryWithOptions(
		clients.K8s,
		5*time.Second,
		k8sinformers.WithNamespace(a.SystemNamespace),
	)
	nvcfFactory := nvcainformers.NewSharedInformerFactory(clients.NVCAOP, 5*time.Second)

	// Get informers
	nsInformer := k8sFactory.Core().V1().Namespaces()
	secretInformer := k8sFactory.Core().V1().Secrets()
	configMapInformer := k8sFactory.Core().V1().ConfigMaps()
	podInformer := k8sFactory.Core().V1().Pods()
	nvcfInformer := nvcfFactory.Nvcf().V1().NVCFBackends()

	// Start informers
	stopCh := make(chan struct{})
	k8sFactory.Start(stopCh)
	nvcfFactory.Start(stopCh)
	k8sFactory.WaitForCacheSync(stopCh)
	nvcfFactory.WaitForCacheSync(stopCh)

	mockBuilder := &BackendK8sCacheBuilder{
		BackendK8sCache: &BackendK8sCache{
			clients:              clients,
			systemNamespace:      a.SystemNamespace,
			operatorNamespace:    a.SystemNamespace,
			ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
			eventBroadcaster:     record.NewBroadcaster(),
			deploymentConfig: nvidiaiov1.DeploymentConfig{
				PriorityClassName:           a.PriorityClassName,
				NodeSelectorKey:             a.NodeSelectorKey,
				NodeSelectorValue:           a.NodeSelectorValue,
				SecretMirrorSourceNamespace: a.NVCASecretMirrorSourceNamespace,
				SecretMirrorLabelSelector:   a.NVCASecretMirrorLabelSelector,
				AdditionalImagePullSecrets:  a.AdditionalImagePullSecrets,
			},
			nvcfWorkerConfig: nvidiaiov1.NVCFWorkerConfig{
				CacheMountOptionsEnabled: a.NVCACacheMountOptionsEnabled,
				CacheMountOptions:        a.NVCACacheMountOptions,
				WorkerDegradationPeriod:  a.NVCAWorkerDegradationPeriod,
			},
			workloadTolerations:    append([]corev1.Toleration(nil), a.NVCAWorkloadTolerations...),
			agentResources:         a.AgentResources,
			webhookResources:       a.WebhookResources,
			otelCollectorResources: a.OTelCollectorResources,
			otelCollectorImageRepo: a.OTelCollectorImageRepo,
			otelCollectorImageTag:  a.OTelCollectorImageTag,
			// Add required informers and listers
			// Set up listers
			nvcfBackendLister: nvcfInformer.Lister(),
			// Set up sync functions
			syncedFuncs: []cache.InformerSynced{
				nvcfInformer.Informer().HasSynced,
				nsInformer.Informer().HasSynced,
				secretInformer.Informer().HasSynced,
				configMapInformer.Informer().HasSynced,
				podInformer.Informer().HasSynced,
			},
			tracer: nvcaoptel.NewTracer(),
			now:    time.Now,
		},
	}

	// Initialize the event broadcaster
	mockBuilder.BackendK8sCache.eventRecorder = mockBuilder.BackendK8sCache.eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "nvca"})

	// Override the agent's builder creation
	ag.backendk8scache = mockBuilder.BackendK8sCache

	// Clean up informers when test is done
	t.Cleanup(func() {
		close(stopCh)
	})

	client, _ := clustermgmt.NewNGCManagedClient(ctx, ag.TokenFetcher, "nvca-operator", nvidiaiov1.EnvTypeStage, clustermgmt.WithRootNGCAPIURL(a.NVCAClusterManagementAPIURL))

	ag.clusterMgmtClient = client

	// Note: Not calling agent.Start() here to avoid duplicate metrics registration
	// The main purpose of this test is to verify agent initialization and configuration

	// Verify that AgentConfig was properly set up
	require.NotNil(t, ag.backendk8scache)
	assert.Equal(t, a.PriorityClassName, ag.backendk8scache.deploymentConfig.PriorityClassName)
	assert.Equal(t, a.NodeSelectorKey, ag.backendk8scache.deploymentConfig.NodeSelectorKey)
	assert.Equal(t, a.NodeSelectorValue, ag.backendk8scache.deploymentConfig.NodeSelectorValue)
	assert.Equal(t, a.NVCASecretMirrorSourceNamespace, ag.backendk8scache.deploymentConfig.SecretMirrorSourceNamespace)
	assert.Equal(t, a.NVCASecretMirrorLabelSelector, ag.backendk8scache.deploymentConfig.SecretMirrorLabelSelector)

	assert.Equal(t, a.NVCACacheMountOptionsEnabled, ag.backendk8scache.nvcfWorkerConfig.CacheMountOptionsEnabled)
	assert.Equal(t, a.NVCACacheMountOptions, ag.backendk8scache.nvcfWorkerConfig.CacheMountOptions)
	assert.Equal(t, a.NVCAWorkerDegradationPeriod, ag.backendk8scache.nvcfWorkerConfig.WorkerDegradationPeriod)
	assert.Equal(t, a.NVCAWorkloadTolerations, ag.backendk8scache.workloadTolerations)

	assert.Equal(t, a.AgentResources, ag.backendk8scache.agentResources)
	assert.Equal(t, a.WebhookResources, ag.backendk8scache.webhookResources)

	// Verify OTel collector fields were properly set up
	assert.Equal(t, a.OTelCollectorImageRepo, ag.backendk8scache.otelCollectorImageRepo)
	assert.Equal(t, a.OTelCollectorImageTag, ag.backendk8scache.otelCollectorImageTag)
	assert.Equal(t, a.OTelCollectorResources, ag.backendk8scache.otelCollectorResources)
}

func newTestContext() context.Context {
	ctx := context.Background()
	// Uncomment the lines below out to enable debug logging.
	// ctx = core.WithDefaultLogger(ctx)
	// log := core.GetLogger(ctx)
	// _ = core.SetLevel(log, "debug")
	return ctx
}

func mockKubeConfigFile(t *testing.T, path string) {
	t.Helper()

	// Create a dummy kubeconfig file
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	// Write minimal kubeconfig content
	_, err = f.WriteString(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://localhost:8443
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
users:
- name: test-user
  user:
    token: test-token`)
	require.NoError(t, err)
}

func TestValidateCIDRs(t *testing.T) {
	tests := []struct {
		name    string
		cidrs   []string
		wantErr bool
	}{
		{
			name:    "empty list",
			cidrs:   []string{},
			wantErr: false,
		},
		{
			name:    "nil list",
			cidrs:   nil,
			wantErr: false,
		},
		{
			name:    "valid single CIDR",
			cidrs:   []string{"192.168.1.0/24"},
			wantErr: false,
		},
		{
			name:    "valid multiple CIDRs",
			cidrs:   []string{"192.168.1.0/24", "10.0.0.0/8", "172.16.0.0/12"},
			wantErr: false,
		},
		{
			name:    "invalid CIDR",
			cidrs:   []string{"192.168.1.0/33"},
			wantErr: true,
		},
		{
			name:    "invalid IP format",
			cidrs:   []string{"256.168.1.0/24"},
			wantErr: true,
		},
		{
			name:    "invalid CIDR format",
			cidrs:   []string{"192.168.1.0"},
			wantErr: true,
		},
		{
			name:    "mixed valid and invalid",
			cidrs:   []string{"192.168.1.0/24", "invalid", "10.0.0.0/8"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCIDRs(tt.cidrs)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// mockClusterMgmtClient is a mock implementation of the clustermgmt.Client interface
type mockClusterMgmtClient struct {
	getClusterFunc func(ctx context.Context, clusterID string) (*clustermgmt.Cluster, error)
}

func (m *mockClusterMgmtClient) GetCluster(ctx context.Context, clusterID string) (*clustermgmt.Cluster, error) {
	if m.getClusterFunc != nil {
		return m.getClusterFunc(ctx, clusterID)
	}
	return nil, errors.New("not implemented")
}

func TestGetAgentEvents(t *testing.T) {
	events := getAgentEvents()
	assert.Len(t, events, 3)
	assert.Contains(t, events, EventTickSyncNVCFBackend)
	assert.Contains(t, events, EventTickFetchNVCACluster)
	assert.Contains(t, events, EventTickSyncNVCACRDS)
}

func TestAgentOptions_SanitizedString(t *testing.T) {
	opts := &AgentOptions{
		KubeConfigPath:                  "/path/to/kubeconfig",
		SystemNamespace:                 "nvca-system",
		SvcAddress:                      "localhost:8080",
		AdminAddr:                       "localhost:9090",
		K8sVersionOverride:              "1.25.8",
		ClusterName:                     "test-cluster",
		ClusterSource:                   nvcaoptypes.ClusterSourceNGCManaged,
		PriorityClassName:               "high-priority",
		NodeSelectorKey:                 "node-type",
		NodeSelectorValue:               "nvca",
		NCAID:                           "test-nca-id",
		NVCFClusterID:                   "cluster-123",
		NVCAClusterManagementAPIURL:     "https://api.test.com",
		NVCAClusterAPIRefreshInterval:   5 * time.Minute,
		NVCAImageRepo:                   "nvcr.io/nvidia/nvca",
		NVCARunAsUserID:                 1000,
		NVCARunAsGroupID:                2000,
		GXCacheNamespace:                "gxcache",
		HelmRepositoryPrefix:            "oci://helm.repo",
		EnableGXCache:                   true,
		DDCSIPAllowList:                 []string{"10.0.0.0/8"},
		K8sClusterNetworkCIDRs:          []string{"192.168.0.0/16"},
		NVCACacheMountOptionsEnabled:    true,
		NVCACacheMountOptions:           "ro,noatime",
		NVCAWorkerDegradationPeriod:     90 * time.Minute,
		NVCASecretMirrorSourceNamespace: "source-ns",
		NVCASecretMirrorLabelSelector:   "mirror=true",
		GenerateImagePullSecret:         true,
	}

	result := opts.sanitizedString()
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "test-cluster")
	assert.Contains(t, result, "nvca-system")
}

func TestAgentOptions_SanitizedString_OTelCollector(t *testing.T) {
	opts := &AgentOptions{
		SystemNamespace:        "nvca-system",
		OTelCollectorImageRepo: "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib",
		OTelCollectorImageTag:  "0.139.0",
		OTelCollectorResources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1000m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
	}

	result := opts.sanitizedString()
	assert.NotEmpty(t, result)
	// Verify OTel collector fields are included in sanitized output
	assert.Contains(t, result, "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib")
	assert.Contains(t, result, "0.139.0")
	assert.Contains(t, result, "OTelCollectorResources")
}

func TestAgentOptions_GetOTelAttributes(t *testing.T) {
	opts := &AgentOptions{
		NCAID:       "test-nca-id",
		ClusterName: "test-cluster",
	}

	attrs := opts.GetOTelAttributes()
	assert.Len(t, attrs, 3)
}

func TestAgentOptions_OTelCollectorFields(t *testing.T) {
	// Test that OTel collector fields can be set and accessed via Agent embedding
	ctx := core.WithDefaultLogger(context.Background())
	opts := &AgentOptions{
		SystemNamespace:        "nvca-system",
		OTelCollectorImageRepo: "custom.io/otel-collector",
		OTelCollectorImageTag:  "v1.2.3",
		OTelCollectorResources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2000m"),
				corev1.ResourceMemory: resource.MustParse("2Gi"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}

	agent, err := NewAgent(ctx, opts)
	require.NoError(t, err)
	assert.NotNil(t, agent)

	// Verify OTel collector fields are accessible via Agent embedding
	assert.Equal(t, "custom.io/otel-collector", agent.OTelCollectorImageRepo)
	assert.Equal(t, "v1.2.3", agent.OTelCollectorImageTag)
	assert.Equal(t, resource.MustParse("2000m"), agent.OTelCollectorResources.Limits[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("2Gi"), agent.OTelCollectorResources.Limits[corev1.ResourceMemory])
	assert.Equal(t, resource.MustParse("500m"), agent.OTelCollectorResources.Requests[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("512Mi"), agent.OTelCollectorResources.Requests[corev1.ResourceMemory])
}

func TestNewAgent_MissingNamespace(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	opts := &AgentOptions{}

	agent, err := NewAgent(ctx, opts)
	assert.Error(t, err)
	assert.Nil(t, agent)
	assert.Contains(t, err.Error(), "SystemNamespace required")
}

func TestNewAgent_InvalidDDCSIPAllowList(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	opts := &AgentOptions{
		SystemNamespace: "nvca-system",
		DDCSIPAllowList: []string{"invalid-cidr"},
	}

	agent, err := NewAgent(ctx, opts)
	assert.Error(t, err)
	assert.Nil(t, agent)
}

func TestNewAgent_InvalidK8sClusterNetworkCIDRs(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	opts := &AgentOptions{
		SystemNamespace:        "nvca-system",
		K8sClusterNetworkCIDRs: []string{"192.168.0.0/33"},
	}

	agent, err := NewAgent(ctx, opts)
	assert.Error(t, err)
	assert.Nil(t, agent)
}

func TestNewAgent_Success(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	opts := &AgentOptions{
		SystemNamespace:               "nvca-system",
		DDCSIPAllowList:               []string{"10.0.0.0/8"},
		K8sClusterNetworkCIDRs:        []string{"192.168.0.0/16"},
		NVCAClusterAPIRefreshInterval: 5 * time.Minute,
	}

	agent, err := NewAgent(ctx, opts)
	assert.NoError(t, err)
	assert.NotNil(t, agent)
	assert.Equal(t, 1, agent.numDispatchers)
	assert.NotNil(t, agent.getTickerEventsFunc)
	assert.NotNil(t, agent.getBackendK8sKubeClientsChFunc)
}

func TestGetBackendK8sKubeClientsCh(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig.yaml")
	mockKubeConfigFile(t, kubeconfigPath)

	opts := &AgentOptions{
		SystemNamespace: "nvca-system",
		KubeConfigPath:  kubeconfigPath,
	}

	agent, err := NewAgent(ctx, opts)
	require.NoError(t, err)

	ch := agent.getBackendK8sKubeClientsCh(ctx)
	assert.NotNil(t, ch)
}

func TestDispatch(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	metricsPrefix := fmt.Sprintf("nvca_test_%d", time.Now().UnixNano())
	ctx = metrics.WithDefaultMetrics(ctx, metricsPrefix, []string{"test", "cluster", "1.0.0"})
	m := metrics.FromContext(ctx)
	t.Cleanup(func() {
		if m != nil {
			m.Destroy()
		}
	})

	opts := &AgentOptions{
		SystemNamespace: "nvca-system",
	}

	agent, err := NewAgent(ctx, opts)
	require.NoError(t, err)

	// Manually initialize the queue maps without starting workers
	agent.resourceEventWorkerQueues = make(map[string]workqueue.TypedRateLimitingInterface[any])
	for _, eventName := range getAgentEvents() {
		agent.resourceEventWorkerQueues[eventName] =
			workqueue.NewTypedRateLimitingQueueWithConfig(
				workqueue.DefaultTypedItemBasedRateLimiter[any](),
				workqueue.TypedRateLimitingQueueConfig[any]{Name: eventName})
	}

	// Test dispatch with ObjectMetaKey
	ev := &core.Event{
		Kind:          EventTickSyncNVCFBackend,
		ObjectMetaKey: "test-ns/test-name",
	}
	agent.dispatch(ctx, ev)

	// Verify queue has the event
	queue := agent.resourceEventWorkerQueues[EventTickSyncNVCFBackend]
	assert.NotNil(t, queue)
	assert.Equal(t, 1, queue.Len())

	// Test dispatch without ObjectMetaKey
	ev2 := &core.Event{
		Kind: EventTickFetchNVCACluster,
	}
	agent.dispatch(ctx, ev2)

	queue2 := agent.resourceEventWorkerQueues[EventTickFetchNVCACluster]
	assert.NotNil(t, queue2)
	assert.Equal(t, 1, queue2.Len())
}

func TestStartEventProcessingWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(core.WithDefaultLogger(context.Background()))
	metricsPrefix := fmt.Sprintf("nvca_test_%d", time.Now().UnixNano())
	ctx = metrics.WithDefaultMetrics(ctx, metricsPrefix, []string{"test", "cluster", "1.0.0"})
	m := metrics.FromContext(ctx)
	t.Cleanup(func() {
		cancel()                          // Cancel context first to stop workers
		time.Sleep(10 * time.Millisecond) // Give workers time to stop
		if m != nil {
			m.Destroy()
		}
	})

	opts := &AgentOptions{
		SystemNamespace: "nvca-system",
	}

	agent, err := NewAgent(ctx, opts)
	require.NoError(t, err)

	err = agent.startEventProcessingWorkers(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, agent.resourceEventWorkerQueues)
	assert.Len(t, agent.resourceEventWorkerQueues, 3)

	// Test calling it twice should fail
	err = agent.startEventProcessingWorkers(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be called twice")
}

func TestStopEventProcessingWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(core.WithDefaultLogger(context.Background()))
	defer cancel()

	metricsPrefix := fmt.Sprintf("nvca_test_%d", time.Now().UnixNano())
	ctx = metrics.WithDefaultMetrics(ctx, metricsPrefix, []string{"test", "cluster", "1.0.0"})
	m := metrics.FromContext(ctx)
	t.Cleanup(func() {
		if m != nil {
			m.Destroy()
		}
	})

	opts := &AgentOptions{
		SystemNamespace: "nvca-system",
	}

	agent, err := NewAgent(ctx, opts)
	require.NoError(t, err)

	err = agent.startEventProcessingWorkers(ctx)
	require.NoError(t, err)

	agent.stopEventProcessingWorkers(ctx)

	// Verify queues are shut down by checking they don't accept new items
	for _, queue := range agent.resourceEventWorkerQueues {
		assert.True(t, queue.ShuttingDown())
	}
}

func TestDispatchReconcileCluster(t *testing.T) {
	tests := []struct {
		name               string
		clusterSource      nvcaoptypes.ClusterSource
		backendk8scacheNil bool
		expectDispatch     bool
	}{
		{
			name:           "non-helm managed cluster",
			clusterSource:  nvcaoptypes.ClusterSourceNGCManaged,
			expectDispatch: false,
		},
		{
			name:               "helm managed but nil cache",
			clusterSource:      nvcaoptypes.ClusterSourceHelmManaged,
			backendk8scacheNil: true,
			expectDispatch:     false,
		},
		{
			name:           "helm managed with cache",
			clusterSource:  nvcaoptypes.ClusterSourceHelmManaged,
			expectDispatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := core.WithDefaultLogger(context.Background())
			metricsPrefix := fmt.Sprintf("nvca_test_%d", time.Now().UnixNano())
			ctx = metrics.WithDefaultMetrics(ctx, metricsPrefix, []string{"test", "cluster", "1.0.0"})
			m := metrics.FromContext(ctx)
			t.Cleanup(func() {
				if m != nil {
					m.Destroy()
				}
			})

			opts := &AgentOptions{
				SystemNamespace: "nvca-system",
				ClusterSource:   tt.clusterSource,
			}

			agent, err := NewAgent(ctx, opts)
			require.NoError(t, err)

			// Manually initialize the queue maps without starting workers
			agent.resourceEventWorkerQueues = make(map[string]workqueue.TypedRateLimitingInterface[any])
			for _, eventName := range getAgentEvents() {
				agent.resourceEventWorkerQueues[eventName] =
					workqueue.NewTypedRateLimitingQueueWithConfig(
						workqueue.DefaultTypedItemBasedRateLimiter[any](),
						workqueue.TypedRateLimitingQueueConfig[any]{Name: eventName})
			}

			if !tt.backendk8scacheNil {
				agent.backendk8scache = &BackendK8sCache{}
			}

			agent.dispatchReconcileCluster(ctx)

			if tt.expectDispatch {
				queue := agent.resourceEventWorkerQueues[EventTickFetchNVCACluster]
				assert.Equal(t, 1, queue.Len())
			}
		})
	}
}

func TestGetTickerEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(core.WithDefaultLogger(context.Background()))
	defer cancel()

	opts := &AgentOptions{
		SystemNamespace:               "nvca-system",
		NVCAClusterAPIRefreshInterval: 100 * time.Millisecond,
	}

	agent, err := NewAgent(ctx, opts)
	require.NoError(t, err)

	eventCh := agent.getTickerEvents(ctx)
	assert.NotNil(t, eventCh)

	// Wait for at least one event
	select {
	case ev := <-eventCh:
		assert.NotNil(t, ev)
		assert.Contains(t, []string{EventTickSyncNVCFBackend, EventTickFetchNVCACluster, EventTickSyncNVCACRDS}, ev.Kind)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for ticker event")
	}
}

func TestSetKubernetesDefaults(t *testing.T) {
	config := &rest.Config{}
	err := setKubernetesDefaults(config)
	assert.NoError(t, err)
	assert.NotNil(t, config.GroupVersion)
	assert.Equal(t, "/api", config.APIPath)
	assert.NotNil(t, config.NegotiatedSerializer)
}

func TestStartDispatcher(t *testing.T) {
	ctx, cancel := context.WithCancel(core.WithDefaultLogger(context.Background()))
	defer cancel()

	metricsPrefix := fmt.Sprintf("nvca_test_%d", time.Now().UnixNano())
	ctx = metrics.WithDefaultMetrics(ctx, metricsPrefix, []string{"test", "cluster", "1.0.0"})
	m := metrics.FromContext(ctx)
	t.Cleanup(func() {
		if m != nil {
			m.Destroy()
		}
	})

	opts := &AgentOptions{
		SystemNamespace: "nvca-system",
	}

	agent, err := NewAgent(ctx, opts)
	require.NoError(t, err)

	// Create a simple event queue
	eventQueue := make(chan *core.Event, 1)

	// Create mock backendk8scache
	clients := mockKubeClients()
	k8sFactory := k8sinformers.NewSharedInformerFactoryWithOptions(
		clients.K8s,
		5*time.Second,
		k8sinformers.WithNamespace(opts.SystemNamespace),
	)
	nvcfFactory := nvcainformers.NewSharedInformerFactory(clients.NVCAOP, 5*time.Second)
	agent.backendk8scache = &BackendK8sCache{
		clients:          clients,
		eventBroadcaster: record.NewBroadcaster(),
		syncedFuncs: []cache.InformerSynced{
			k8sFactory.Core().V1().Namespaces().Informer().HasSynced,
		},
		nvcfBackendLister: nvcfFactory.Nvcf().V1().NVCFBackends().Lister(),
	}
	agent.backendk8scache.eventRecorder = agent.backendk8scache.eventBroadcaster.NewRecorder(
		scheme.Scheme,
		corev1.EventSource{Component: "nvca"},
	)

	// Start dispatcher in background
	go agent.startDispatcher(ctx, eventQueue)

	// Give it a moment to start
	time.Sleep(10 * time.Millisecond)

	// Cancel context to stop the dispatcher
	cancel()

	// Give it time to stop
	time.Sleep(10 * time.Millisecond)
}

func TestDispatch_UnknownEvent(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	metricsPrefix := fmt.Sprintf("nvca_test_%d", time.Now().UnixNano())
	ctx = metrics.WithDefaultMetrics(ctx, metricsPrefix, []string{"test", "cluster", "1.0.0"})
	m := metrics.FromContext(ctx)
	t.Cleanup(func() {
		if m != nil {
			m.Destroy()
		}
	})

	opts := &AgentOptions{
		SystemNamespace: "nvca-system",
	}

	agent, err := NewAgent(ctx, opts)
	require.NoError(t, err)

	// Manually initialize the queue maps without starting workers
	agent.resourceEventWorkerQueues = make(map[string]workqueue.TypedRateLimitingInterface[any])
	for _, eventName := range getAgentEvents() {
		agent.resourceEventWorkerQueues[eventName] =
			workqueue.NewTypedRateLimitingQueueWithConfig(
				workqueue.DefaultTypedItemBasedRateLimiter[any](),
				workqueue.TypedRateLimitingQueueConfig[any]{Name: eventName})
	}

	// Test dispatch with unknown event type (should log error but not crash)
	ev := &core.Event{
		Kind: "UNKNOWN_EVENT_TYPE",
	}
	agent.dispatch(ctx, ev)

	// Verify no queue has the event
	for _, queue := range agent.resourceEventWorkerQueues {
		assert.Equal(t, 0, queue.Len())
	}
}

func TestShutdownHandler_SentinelNotBeingDeleted(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clients := mockKubeClients()

	// Create sentinel ConfigMap without deletionTimestamp
	sentinelCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvca-operator-shutdown-sentinel",
			Namespace: NVCAOperatorNamespace,
		},
	}
	_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, sentinelCM, metav1.CreateOptions{})
	require.NoError(t, err)

	opts := &AgentOptions{
		SystemNamespace: NVCAOperatorNamespace,
	}
	agent, err := NewAgent(ctx, opts)
	require.NoError(t, err)

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	w := httptest.NewRecorder()

	// Call handler - with polling, this will poll and then return no deletion
	handler := cleanup.NewShutdownHandler(ctx, cleanup.ShutdownHandlerOptions{
		K8sClient:     clients.K8s,
		NVCAClient:    clients.NVCAOP,
		DynamicClient: clients.DynamicClient,
		Namespace:     NVCAOperatorNamespace,
		PollTimeout:   100 * time.Millisecond, // Short timeout for tests
		OnShutdown:    agent.StopEventProcessing,
	})
	handler(w, req)

	// Verify response - with polling, message is "no deletion detected, normal restart"
	assert.Equal(t, http.StatusOK, w.Code)

	var resp cleanup.ShutdownResponse
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.False(t, resp.Cleanup)
	assert.Equal(t, "no deletion detected, normal restart", resp.Message)
}

func TestShutdownHandler_SentinelBeingDeleted(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clients := mockKubeClients()

	// Create sentinel ConfigMap with deletionTimestamp
	now := metav1.Now()
	sentinelCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nvca-operator-shutdown-sentinel",
			Namespace:         NVCAOperatorNamespace,
			DeletionTimestamp: &now,
			Finalizers:        []string{"nvca.nvcf.nvidia.io/operator-cleanup"},
		},
	}
	_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, sentinelCM, metav1.CreateOptions{})
	require.NoError(t, err)

	opts := &AgentOptions{
		SystemNamespace: NVCAOperatorNamespace,
	}
	agent, err := NewAgent(ctx, opts)
	require.NoError(t, err)

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	w := httptest.NewRecorder()

	// Call handler
	handler := cleanup.NewShutdownHandler(ctx, cleanup.ShutdownHandlerOptions{
		K8sClient:     clients.K8s,
		NVCAClient:    clients.NVCAOP,
		DynamicClient: clients.DynamicClient,
		Namespace:     NVCAOperatorNamespace,
		OnShutdown:    agent.StopEventProcessing,
	})
	handler(w, req)

	// Verify response
	assert.Equal(t, http.StatusOK, w.Code)

	var resp cleanup.ShutdownResponse
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.True(t, resp.Cleanup)
	assert.Equal(t, "cleanup complete", resp.Message)
}

func TestShutdownHandler_SentinelNotFound(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clients := mockKubeClients()

	// Don't create sentinel - it doesn't exist

	opts := &AgentOptions{
		SystemNamespace: NVCAOperatorNamespace,
	}
	agent, err := NewAgent(ctx, opts)
	require.NoError(t, err)

	// Create request
	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	w := httptest.NewRecorder()

	// Call handler
	handler := cleanup.NewShutdownHandler(ctx, cleanup.ShutdownHandlerOptions{
		K8sClient:     clients.K8s,
		NVCAClient:    clients.NVCAOP,
		DynamicClient: clients.DynamicClient,
		Namespace:     NVCAOperatorNamespace,
		OnShutdown:    agent.StopEventProcessing,
	})
	handler(w, req)

	// Verify response - sentinel not found is treated as being deleted
	assert.Equal(t, http.StatusOK, w.Code)

	var resp cleanup.ShutdownResponse
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.True(t, resp.Cleanup)
	assert.Equal(t, "cleanup complete", resp.Message)
}

func TestShutdownHandler_Idempotent(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clients := mockKubeClients()

	// Create sentinel ConfigMap with deletionTimestamp
	now := metav1.Now()
	sentinelCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nvca-operator-shutdown-sentinel",
			Namespace:         NVCAOperatorNamespace,
			DeletionTimestamp: &now,
			Finalizers:        []string{"nvca.nvcf.nvidia.io/operator-cleanup"},
		},
	}
	_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, sentinelCM, metav1.CreateOptions{})
	require.NoError(t, err)

	opts := &AgentOptions{
		SystemNamespace: NVCAOperatorNamespace,
	}
	agent, err := NewAgent(ctx, opts)
	require.NoError(t, err)

	handler := cleanup.NewShutdownHandler(ctx, cleanup.ShutdownHandlerOptions{
		K8sClient:     clients.K8s,
		NVCAClient:    clients.NVCAOP,
		DynamicClient: clients.DynamicClient,
		Namespace:     NVCAOperatorNamespace,
		OnShutdown:    agent.StopEventProcessing,
	})

	// Call handler multiple times
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
		w := httptest.NewRecorder()
		handler(w, req)

		// All calls should succeed
		assert.Equal(t, http.StatusOK, w.Code, "call %d failed", i+1)
	}
}
