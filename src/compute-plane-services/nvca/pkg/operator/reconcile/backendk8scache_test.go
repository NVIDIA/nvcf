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
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/undefinedlabs/go-mpatch"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	fakeapiextensionclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/record"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	fakenvcaopclient "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/scheme"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/cleanup"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/reconcile/clustermgmt"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

var icmsGVK schema.GroupVersionKind

func init() {
	icmsCRD := makeICMSRequestCRD()
	icmsGVK = schema.GroupVersionKind{
		Group:   icmsCRD.Spec.Group,
		Version: icmsCRD.Spec.Versions[0].Name,
		Kind:    icmsCRD.Spec.Names.Kind,
	}

	// Initialize a shared test scheme that all test clients will use
	testScheme := runtime.NewScheme()
	fakek8sclient.AddToScheme(testScheme)
	scheme.AddToScheme(testScheme)
	metav1.AddToGroupVersion(testScheme, nvidiaiov1.SchemeGroupVersion)
	testScheme.AddKnownTypes(nvidiaiov1.SchemeGroupVersion,
		&nvidiaiov1.NVCFBackend{},
		&nvidiaiov1.NVCFBackendList{},
	)
	newDynamicClient = func(_ *runtime.Scheme, _ *rest.Config) (dynamic.Interface, error) {
		return fakedynamic.NewSimpleDynamicClient(testScheme), nil
	}

	newDiscoverClient = func(_ kubernetes.Interface, _ *rest.Config) (discovery.DiscoveryInterface, error) {
		// Create a new fake client with our test scheme
		client := fakek8sclient.NewSimpleClientset()
		// Add the required API resources
		discClient := client.Discovery().(*fakediscovery.FakeDiscovery)
		icmsCRD := makeICMSRequestCRD()
		icmsGVK := schema.GroupVersionKind{
			Group:   icmsCRD.Spec.Group,
			Version: icmsCRD.Spec.Versions[0].Name,
			Kind:    icmsCRD.Spec.Names.Kind,
		}
		discClient.Resources = []*metav1.APIResourceList{
			{
				GroupVersion: nvidiaiov1.SchemeGroupVersion.String(),
				APIResources: []metav1.APIResource{
					{Name: "nvcfbackends", Namespaced: true, Kind: "NVCFBackend"},
				},
			},
			{
				GroupVersion: icmsGVK.GroupVersion().String(),
				APIResources: []metav1.APIResource{
					{Name: "icmsrequests", SingularName: "icmsrequest", Namespaced: true, Group: icmsGVK.Group, Version: icmsGVK.Version, Kind: icmsGVK.Kind},
				},
			},
		}
		return discClient, nil
	}
}

func getTestNVCFBackendAllFeatures() *nvidiaiov1.NVCFBackend {
	return &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcf-dgx-cloud-stg",
			Namespace: NVCAOperatorNamespace,
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			Overrides: &nvidiaiov1.NVCFBackendSpecT{
				Version: "v1.26.7",
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					PullSecretName: "nvca-override-image-pull-secret",
				},
			},
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "v1.26.6",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "nvcf-dgx-cloud-test-cluster",
					ClusterGroupName: "nvcf-dgx-cloud-test-cluster-group",
					SvcAddress:       "127.0.0.1:9000",
					AdminAddr:        "127.0.0.1:9001",
				},
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					Repository: "local/nvcaop",
					Tag:        "latest",
				},
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{
						"LogPosting",
						"CachingSupport",
						"PeriodicInstanceStatusUpdate",
					},
					OTELConfig: &nvidiaiov1.OTELConfig{
						ServiceName: nvcaoptypes.NVCAModuleName,
						Endpoint:    "otel.test.nvidia.com:8282",
						AccessToken: "randomkey",
					},
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "randomID",
					ClientSecretKey: "randomSecretKey",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
				WebhookConfig: nvidiaiov1.WebhookConfig{
					ListenPort:  9002,
					ServicePort: 9003,
					ImageConfig: nvidiaiov1.ImageConfig{
						Repository: "local/webhook-server",
						Tag:        "latest",
						PullPolicy: "Never",
					},
				},
				AgentConfig: nvidiaiov1.AgentConfig{
					NVCFWorkerConfig: nvidiaiov1.NVCFWorkerConfig{
						CacheMountOptionsEnabled: true,
						CacheMountOptions:        types.DefaultCacheCSIMountOptions,
						WorkerDegradationPeriod:  90 * time.Minute,
					},
				},
			},
		},
	}
}

func getTestNVCFBackendAllFeaturesWithCSIOptionOverrides() *nvidiaiov1.NVCFBackend {
	return &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcf-dgx-cloud-stg",
			Namespace: NVCAOperatorNamespace,
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			Overrides: &nvidiaiov1.NVCFBackendSpecT{
				Version: "v1.26.7",
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					PullSecretName: "nvca-override-image-pull-secret",
				},
			},
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "v1.26.6",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "nvcf-dgx-cloud-test-cluster",
					ClusterGroupName: "nvcf-dgx-cloud-test-cluster-group",
					SvcAddress:       "127.0.0.1:9000",
					AdminAddr:        "127.0.0.1:9001",
				},
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					Repository: "local/nvcaop",
					Tag:        "latest",
				},
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{
						"LogPosting",
						"CachingSupport",
						"PeriodicInstanceStatusUpdate",
					},
					OTELConfig: &nvidiaiov1.OTELConfig{
						ServiceName: nvcaoptypes.NVCAModuleName,
						Endpoint:    "otel.test.nvidia.com:8282",
						AccessToken: "randomkey",
					},
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "randomID",
					ClientSecretKey: "randomSecretKey",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
				WebhookConfig: nvidiaiov1.WebhookConfig{
					ListenPort:  9002,
					ServicePort: 9003,
					ImageConfig: nvidiaiov1.ImageConfig{
						Repository: "local/webhook-server",
						Tag:        "latest",
						PullPolicy: "Never",
					},
				},
				AgentConfig: nvidiaiov1.AgentConfig{
					NVCFWorkerConfig: nvidiaiov1.NVCFWorkerConfig{
						CacheMountOptionsEnabled: true,
						CacheMountOptions:        "remount,rw",
						WorkerDegradationPeriod:  30 * time.Minute,
					},
				},
			},
		},
	}
}

func getTestNVCFBackendAllFeaturesWithCSIMountDisabled() *nvidiaiov1.NVCFBackend {
	return &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcf-dgx-cloud-stg",
			Namespace: NVCAOperatorNamespace,
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			Overrides: &nvidiaiov1.NVCFBackendSpecT{
				Version: "2.45.0",
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					PullSecretName: "nvca-override-image-pull-secret",
				},
			},
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.45.0",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "nvcf-dgx-cloud-test-cluster",
					ClusterGroupName: "nvcf-dgx-cloud-test-cluster-group",
					SvcAddress:       "127.0.0.1:9000",
					AdminAddr:        "127.0.0.1:9001",
				},
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					Repository: "local/nvcaop",
					Tag:        "latest",
				},
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{
						"LogPosting",
						"CachingSupport",
						"PeriodicInstanceStatusUpdate",
					},
					OTELConfig: &nvidiaiov1.OTELConfig{
						ServiceName: nvcaoptypes.NVCAModuleName,
						Endpoint:    "otel.test.nvidia.com:8282",
						AccessToken: "randomkey",
					},
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "randomID",
					ClientSecretKey: "randomSecretKey",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
				WebhookConfig: nvidiaiov1.WebhookConfig{
					ListenPort:  9002,
					ServicePort: 9003,
					ImageConfig: nvidiaiov1.ImageConfig{
						Repository: "local/webhook-server",
						Tag:        "latest",
						PullPolicy: "Never",
					},
				},
				AgentConfig: nvidiaiov1.AgentConfig{
					NVCFWorkerConfig: nvidiaiov1.NVCFWorkerConfig{
						CacheMountOptionsEnabled: false,
						CacheMountOptions:        "",
						WorkerDegradationPeriod:  90 * time.Minute,
					},
				},
			},
		},
	}
}

func getTestNVCFBackendBCP() *nvidiaiov1.NVCFBackend {
	return &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcf-dgx-cloud-stg",
			Namespace: NVCAOperatorNamespace,
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			Overrides: &nvidiaiov1.NVCFBackendSpecT{
				Version: "v1.26.7",
			},
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "v1.26.6",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "nvcf-dgx-cloud-test-cluster",
					ClusterGroupName: "nvcf-dgx-cloud-test-cluster-group",
					BackendType:      "bcp",
					GPUDiscovery: nvidiaiov1.GPUDiscoveryConfig{
						Static: &nvidiaiov1.StaticGPUDiscoveryConfig{
							AllocatedGPUCapacity: 2,
						},
					},
				},
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					Repository: "local/nvcaop",
					Tag:        "latest",
				},
				FeatureGate: nvidiaiov1.FeatureGate{
					OTELConfig: &nvidiaiov1.OTELConfig{
						ServiceName: nvcaoptypes.NVCAModuleName,
						Endpoint:    "otel.test.nvidia.com:8282",
						AccessToken: "randomkey",
					},
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "randomID",
					ClientSecretKey: "randomSecretKey",
				},
				VaultConfig: nvidiaiov1.VaultConfig{
					Enabled: false,
				},
			},
		},
	}
}

func getTestNVCFBackendMinimal() *nvidiaiov1.NVCFBackend {
	return &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcf-dgx-cloud-stg",
			Namespace: NVCAOperatorNamespace,
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			Overrides: &nvidiaiov1.NVCFBackendSpecT{
				Version: "v1.26.7",
			},
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "v1.26.6",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "nvcf-dgx-cloud-test-cluster",
					ClusterGroupName: "nvcf-dgx-cloud-test-cluster-group",
					GPUDiscovery: nvidiaiov1.GPUDiscoveryConfig{
						Static: &nvidiaiov1.StaticGPUDiscoveryConfig{
							AllocatedGPUCapacity: 2,
						},
					},
				},
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					Repository: "local/nvcaop",
					Tag:        "latest",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "randomID",
					ClientSecretKey: "randomkey",
				},
			},
		},
	}
}

func getTestNVCFBackendMinimalExternal() *nvidiaiov1.NVCFBackend {
	return &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcf-dgx-cloud-stg",
			Namespace: NVCAOperatorNamespace,
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			Overrides: &nvidiaiov1.NVCFBackendSpecT{
				Version: "v1.26.7",
			},
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "v1.26.6",
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "nvcf-dgx-cloud-test-cluster",
					ClusterGroupName: "nvcf-dgx-cloud-test-cluster-group",
					GPUDiscovery: nvidiaiov1.GPUDiscoveryConfig{
						Static: &nvidiaiov1.StaticGPUDiscoveryConfig{
							AllocatedGPUCapacity: 2,
						},
					},
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:        "test-oauth-client-id",
					ClientSecretKey: "test-oauth-client-secret-key",
				},
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					Repository: "local/nvcaop",
					Tag:        "latest",
				},
			},
		},
	}
}

func newStaticGPUSConfigMap() *v1.ConfigMap {
	return &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvca-static-gpus",
			Namespace: NVCAOperatorNamespace,
		},
		Data: map[string]string{
			"gpus": `[
				{
					"name": "L40",
					"instanceTypes": [
						{
							"name": "ON-PREM.GPU.L40_1x",
							"value": "ON-PREM.GPU.L40",
							"description": "One Nvidia ada GPU",
							"default": true,
							"cpuCores": 4,
							"systemMemory": "28G",
							"gpuMemory": "48G",
							"gpuCount": 1
						},
						{
							"name": "ON-PREM.GPU.L40_2x",
							"value": "ON-PREM.GPU.L40",
							"description": "Two Nvidia ada GPUs",
							"default": false,
							"cpuCores": 4,
							"systemMemory": "28G",
							"gpuMemory": "96G",
							"gpuCount": 2
						}
					]
				}
			]`,
		},
	}
}

func newRESTConfig() *rest.Config {
	cfg := &rest.Config{Host: "http://test.k8s.local"}
	cfg.CAData = []byte("localKubeconfigCA")
	return cfg
}

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(icmsGVK, &nvcav2beta1.ICMSRequest{})
	s.AddKnownTypeWithName(icmsGVK.GroupVersion().WithKind(icmsGVK.Kind+"List"), &nvcav2beta1.ICMSRequestList{})
	return s
}

func mockKubeClientsForIntegrationTests() *kubeclients.KubeClients {
	scheme := newTestScheme()
	k8sClient := fakek8sclient.NewSimpleClientset(newStaticGPUSConfigMap())
	discClient := k8sClient.Discovery().(*fakediscovery.FakeDiscovery)
	icmsCRD := makeICMSRequestCRD()
	icmsGVK := schema.GroupVersionKind{
		Group:   icmsCRD.Spec.Group,
		Version: icmsCRD.Spec.Versions[0].Name,
		Kind:    icmsCRD.Spec.Names.Kind,
	}
	for _, rl := range []*metav1.APIResourceList{
		{
			GroupVersion: icmsGVK.GroupVersion().String(),
			APIResources: []metav1.APIResource{{
				Name: "icmsrequests", SingularName: "icmsrequest", Namespaced: true,
				Group: icmsGVK.Group, Version: icmsGVK.Version, Kind: icmsGVK.Kind,
				Verbs: metav1.Verbs{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection", "proxy"},
			}},
		},
	} {
		discClient.Resources = append(discClient.Resources, rl)
	}
	grs, err := restmapper.GetAPIGroupResources(discClient)
	if err != nil {
		panic(err)
	}
	return &kubeclients.KubeClients{
		Config:              newRESTConfig(),
		NVCAOP:              fakenvcaopclient.NewSimpleClientset(),
		K8s:                 k8sClient,
		APIExtV1:            fakeapiextensionclient.NewSimpleClientset().ApiextensionsV1(),
		DynamicClient:       fakedynamic.NewSimpleDynamicClient(scheme),
		DiscoveryClient:     discClient,
		DiscoveryRESTMapper: restmapper.NewDiscoveryRESTMapper(grs),
	}
}

func mockKubeClients() *kubeclients.KubeClients {
	scheme := newTestScheme()
	k8sClient := fakek8sclient.NewSimpleClientset(
		newStaticGPUSConfigMap(),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      nvcfCustomAnnotationsConfigMapName,
				Namespace: NVCAOperatorNamespace,
			},
			Data: map[string]string{},
		},
	)
	discClient := k8sClient.Discovery().(*fakediscovery.FakeDiscovery)
	icmsCRD := makeICMSRequestCRD()
	icmsGVK := schema.GroupVersionKind{
		Group:   icmsCRD.Spec.Group,
		Version: icmsCRD.Spec.Versions[0].Name,
		Kind:    icmsCRD.Spec.Names.Kind,
	}
	for _, rl := range []*metav1.APIResourceList{
		{
			GroupVersion: icmsGVK.GroupVersion().String(),
			APIResources: []metav1.APIResource{{
				Name: "icmsrequests", SingularName: "icmsrequest", Namespaced: true,
				Group: icmsGVK.Group, Version: icmsGVK.Version, Kind: icmsGVK.Kind,
				Verbs: metav1.Verbs{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection", "proxy"},
			}},
		},
	} {
		discClient.Resources = append(discClient.Resources, rl)
	}
	grs, err := restmapper.GetAPIGroupResources(discClient)
	if err != nil {
		panic(err)
	}
	return &kubeclients.KubeClients{
		Config:              newRESTConfig(),
		NVCAOP:              fakenvcaopclient.NewSimpleClientset(),
		K8s:                 k8sClient,
		APIExtV1:            fakeapiextensionclient.NewSimpleClientset().ApiextensionsV1(),
		DynamicClient:       fakedynamic.NewSimpleDynamicClient(scheme),
		DiscoveryClient:     discClient,
		DiscoveryRESTMapper: restmapper.NewDiscoveryRESTMapper(grs),
	}
}

func TestBackendK8sSyncMinimal(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	log := core.GetLogger(ctx)
	core.SetLevel(log, "debug")
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	fakeExit := func(int) {}

	p := PatchOSExit(t, fakeExit)
	t.Cleanup(p.Unpatch)

	agentOpts := AgentOptions{
		KubeConfigPath:  "test/kubeconfig.yaml",
		SystemNamespace: NVCAOperatorNamespace,
		SvcAddress:      "localhost",
		AdminAddr:       "localhost",
		ClusterSource:   nvcaoptypes.ClusterSourceNGCManaged,
	}
	_, err := NewAgent(ctx, &agentOpts)
	assert.NoError(t, err)

	agentOpts = AgentOptions{
		KubeConfigPath:     "test/kubeconfig.yaml",
		SystemNamespace:    NVCAOperatorNamespace,
		SvcAddress:         "localhost",
		AdminAddr:          "localhost",
		K8sVersionOverride: "1.25.8",
		TokenFetcher:       &mockTokenFetcher{token: "randomkey"},
		ClusterSource:      nvcaoptypes.ClusterSourceNGCManaged,
	}
	ag, err := NewAgent(ctx, &agentOpts)
	require.NoError(t, err)

	healthzGPUUsage := map[nvidiaiov1.GPUName]nvidiaiov1.GPUResource{
		"A100": {
			Capacity:  1,
			Available: 0,
			Allocated: 1,
		},
	}
	nvcaServerHealthz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		json.NewEncoder(w).Encode(nvcfBackendHealthResponse{
			Status:     nvidiaiov1.AgentStatusHealthy,
			K8sVersion: "v1.27.0",
			GPUUsage:   healthzGPUUsage,
		})
	}))
	t.Cleanup(nvcaServerHealthz.Close)
	tmpMakeHealthzURL := makeNVCAHealthzURL
	makeNVCAHealthzURL = func(nb *nvidiaiov1.NVCFBackend) (string, error) {
		return nvcaServerHealthz.URL, nil
	}
	t.Cleanup(func() { makeNVCAHealthzURL = tmpMakeHealthzURL })

	// dummy backendk8s
	clients := mockKubeClients()
	b := NewBackendK8sCacheBuilder().
		WithSystemNamespace(agentOpts.SystemNamespace).
		WithAgentConfig(nvidiaiov1.AgentConfig{
			DeploymentConfig: nvidiaiov1.DeploymentConfig{
				PriorityClassName: agentOpts.PriorityClassName,
				NodeSelectorKey:   "nodename",
				NodeSelectorValue: "k8snode",
			},
		}).
		WithUIDGIDOverride(1729, 1729).
		WithHelmRepositoryPrefix("nvidia/nvcf-byoc").
		WithGXCache(clustermgmt.DefaultGXCacheNamespace, false).
		WithK8sClusterNetworkCIDRs([]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/12"}).
		WithNGCServiceKeyFetcher(ag.TokenFetcher).
		WithClients(clients)

	bc, _, err := b.Start(ctx)
	require.NoError(t, err)

	ag.backendk8scache = bc

	nb := getTestNVCFBackendMinimal()

	_, err = bc.clients.NVCAOP.NvcfV1().NVCFBackends(agentOpts.SystemNamespace).Create(ctx, nb, metav1.CreateOptions{})
	require.NoError(t, err)

	var (
		svc *v1.Service
		dep *appsv1.Deployment
	)
	verifyNVCFBackendSynced := func(ct *assert.CollectT) {
		err = bc.SyncAllNVCFBackends(ctx)
		require.NoError(ct, err)

		_, err = bc.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, ICMSRequestCRDName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ResourceQuotas(getSystemNamespace(nb)).Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.NoError(ct, err)

		ns, err := bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.NoError(ct, err)
		assert.NotContains(t, ns.Labels, clustermgmt.ShaderCacheLabelKey)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, NetworkPoliciesConfigmapName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, NVCAVaultConfigmapName, metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAImagePullSecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.RbacV1().ClusterRoles().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.RbacV1().ClusterRoleBindings().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, MiniServiceRBACConfigmapName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAWebhookTLSCertSecretName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAWebhookTLSCASecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, nvcfCustomAnnotationsConfigMapName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NGCServiceAPIKeySecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OTELConfigSecretName, metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))

		svc, err = bc.clients.K8s.CoreV1().Services(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		dep, err = bc.clients.K8s.AppsV1().Deployments(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		nbObj, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(agentOpts.SystemNamespace).Get(ctx, nb.Name, metav1.GetOptions{})
		require.NoError(ct, err)
		require.Equal(ct, "v1.26.6", nbObj.Spec.Version)
		require.Equal(ct, nbObj.Spec.Overrides.Version, nbObj.Status.Version)
		require.Equal(ct, "v1.27.0", nbObj.Status.KubernetesVersion)
		require.NotNil(ct, nbObj.Status.LastUpdated)
		require.NotNil(ct, nbObj.Status.LastUpdatedAgentStatus)
	}
	require.EventuallyWithT(t, verifyNVCFBackendSynced, 60*time.Second, 5*time.Second)

	assert.Len(t, svc.Spec.Ports, 2)
	assert.EqualValues(t, 8000, svc.Spec.Ports[0].Port)
	assert.Equal(t, intstr.FromString("http"), svc.Spec.Ports[0].TargetPort)
	assert.EqualValues(t, 8443, svc.Spec.Ports[1].Port)
	assert.Equal(t, intstr.FromString("webhook-https"), svc.Spec.Ports[1].TargetPort)

	assert.Len(t, dep.Spec.Template.Spec.Containers, 2)
	assert.EqualValues(t, 8000, dep.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort)
	assert.NotContains(t, dep.Spec.Template.Spec.Containers[0].Args, featureflag.GXCache.Key)
	assert.Equal(t, "http", dep.Spec.Template.Spec.Containers[0].Ports[0].Name)
	assert.Equal(t, NVCAWebhookContainerName, dep.Spec.Template.Spec.Containers[1].Name)
	assert.NotEqual(t, DefaultNVCARunAsUserID, *dep.Spec.Template.Spec.SecurityContext.RunAsUser)
	assert.NotEqual(t, DefaultNVCARunAsGroupID, *dep.Spec.Template.Spec.SecurityContext.RunAsGroup)
	assert.Equal(t, "nvcr.io/nvidia/nvcf-byoc/nvca:v1.26.7", dep.Spec.Template.Spec.Containers[1].Image)
	assert.Equal(t, v1.PullIfNotPresent, dep.Spec.Template.Spec.Containers[1].ImagePullPolicy)
	assert.EqualValues(t, 8443, dep.Spec.Template.Spec.Containers[1].Ports[0].ContainerPort)
	assert.Equal(t, "webhook-https", dep.Spec.Template.Spec.Containers[1].Ports[0].Name)

	// Cleanup resources - ICMSRequests are cleaned up (finalizers removed, then deleted)
	// and namespaces are deleted. CRDs are cleaned via owner references, not by cleanupResources.

	err = bc.cleanupResources(ctx, nb)
	require.NoError(t, err)

	verifyNVCFBackendDeleted := func(ct *assert.CollectT) {
		// Note: CRDs are cleaned via owner references when the NVCFBackend CRD is deleted by Helm,
		// not by cleanupResources, so we don't check for CRD deletion here.

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))
	}
	require.EventuallyWithT(t, verifyNVCFBackendDeleted, 60*time.Second, 5*time.Second)
}

func TestBackendK8sSyncMinimalExternal(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	fakeExit := func(int) {}

	p := PatchOSExit(t, fakeExit)
	t.Cleanup(p.Unpatch)

	agentOpts := AgentOptions{
		KubeConfigPath:  "test/kubeconfig.yaml",
		SystemNamespace: NVCAOperatorNamespace,
		SvcAddress:      "localhost",
		AdminAddr:       "localhost",
	}
	_, err := NewAgent(ctx, &agentOpts)
	assert.NoError(t, err)

	agentOpts = AgentOptions{
		KubeConfigPath:     "test/kubeconfig.yaml",
		SystemNamespace:    NVCAOperatorNamespace,
		SvcAddress:         "localhost",
		AdminAddr:          "localhost",
		K8sVersionOverride: "1.25.8",
		TokenFetcher:       &mockTokenFetcher{token: "randomkey"},
		NVCARunAsUserID:    DefaultNVCARunAsUserID,
		NVCARunAsGroupID:   DefaultNVCARunAsGroupID,
	}
	ag, err := NewAgent(ctx, &agentOpts)
	require.NoError(t, err)

	healthzGPUUsage := map[nvidiaiov1.GPUName]nvidiaiov1.GPUResource{
		"A100": {
			Capacity:  1,
			Available: 0,
			Allocated: 1,
		},
	}
	nvcaServerHealthz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		json.NewEncoder(w).Encode(nvcfBackendHealthResponse{
			Status:     nvidiaiov1.AgentStatusHealthy,
			K8sVersion: "v1.27.0",
			GPUUsage:   healthzGPUUsage,
		})
	}))
	t.Cleanup(nvcaServerHealthz.Close)
	tmpMakeHealthzURL := makeNVCAHealthzURL
	makeNVCAHealthzURL = func(nb *nvidiaiov1.NVCFBackend) (string, error) {
		return nvcaServerHealthz.URL, nil
	}
	t.Cleanup(func() { makeNVCAHealthzURL = tmpMakeHealthzURL })

	// dummy backendk8s
	clients := mockKubeClients()
	b := NewBackendK8sCacheBuilder().
		WithSystemNamespace(agentOpts.SystemNamespace).
		WithAgentConfig(nvidiaiov1.AgentConfig{
			DeploymentConfig: nvidiaiov1.DeploymentConfig{
				PriorityClassName: agentOpts.PriorityClassName,
				NodeSelectorKey:   "nodename",
				NodeSelectorValue: "k8snode",
			},
		}).
		WithUIDGIDOverride(ag.NVCARunAsUserID, ag.NVCARunAsGroupID).
		WithK8sClusterNetworkCIDRs([]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/12"}).
		WithNGCServiceKeyFetcher(ag.TokenFetcher).
		WithClients(clients)

	bc, _, err := b.Start(ctx)
	require.NoError(t, err)

	ag.backendk8scache = bc

	nb := getTestNVCFBackendMinimalExternal()

	_, err = bc.clients.NVCAOP.NvcfV1().NVCFBackends(agentOpts.SystemNamespace).Create(ctx, nb, metav1.CreateOptions{})
	require.NoError(t, err)

	var (
		svc *v1.Service
		dep *appsv1.Deployment
	)
	verifyNVCFBackendSynced := func(ct *assert.CollectT) {
		err = bc.SyncAllNVCFBackends(ctx)
		require.NoError(ct, err)

		_, err = bc.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, ICMSRequestCRDName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, StorageRequestCRDName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ResourceQuotas(getSystemNamespace(nb)).Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.NoError(ct, err)

		ns, err := bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.NoError(ct, err)
		assert.NotContains(t, ns.Labels, clustermgmt.ShaderCacheLabelKey)

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, NetworkPoliciesConfigmapName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, NVCAVaultConfigmapName, metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAImagePullSecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.RbacV1().ClusterRoles().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.RbacV1().ClusterRoleBindings().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, MiniServiceRBACConfigmapName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAWebhookTLSCertSecretName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAWebhookTLSCASecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NGCServiceAPIKeySecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OTELConfigSecretName, metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))

		svc, err = bc.clients.K8s.CoreV1().Services(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		dep, err = bc.clients.K8s.AppsV1().Deployments(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, nvcfCustomAnnotationsConfigMapName, metav1.GetOptions{})
		require.NoError(ct, err)

		nbObj, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(agentOpts.SystemNamespace).Get(ctx, nb.Name, metav1.GetOptions{})
		require.NoError(ct, err)
		require.Equal(ct, "v1.26.6", nbObj.Spec.Version)
		require.Equal(ct, nbObj.Spec.Overrides.Version, nbObj.Status.Version)
		require.Equal(ct, "v1.27.0", nbObj.Status.KubernetesVersion)
		require.NotNil(ct, nbObj.Status.LastUpdated)
		require.NotNil(ct, nbObj.Status.LastUpdatedAgentStatus)
	}
	require.EventuallyWithT(t, verifyNVCFBackendSynced, 60*time.Second, 5*time.Second)

	assert.Len(t, svc.Spec.Ports, 2)
	assert.EqualValues(t, 8000, svc.Spec.Ports[0].Port)
	assert.Equal(t, intstr.FromString("http"), svc.Spec.Ports[0].TargetPort)
	assert.EqualValues(t, 8443, svc.Spec.Ports[1].Port)
	assert.Equal(t, intstr.FromString("webhook-https"), svc.Spec.Ports[1].TargetPort)

	assert.Len(t, dep.Spec.Template.Spec.Containers, 2)
	assert.EqualValues(t, 8000, dep.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort)
	assert.NotContains(t, dep.Spec.Template.Spec.Containers[0].Args, featureflag.GXCache.Key)
	assert.Equal(t, "http", dep.Spec.Template.Spec.Containers[0].Ports[0].Name)
	assert.Equal(t, NVCAWebhookContainerName, dep.Spec.Template.Spec.Containers[1].Name)
	assert.Equal(t, "nvcr.io/nvidia/nvcf-byoc/nvca:v1.26.7", dep.Spec.Template.Spec.Containers[1].Image)
	assert.Equal(t, DefaultNVCARunAsUserID, *dep.Spec.Template.Spec.SecurityContext.RunAsUser)
	assert.Equal(t, DefaultNVCARunAsGroupID, *dep.Spec.Template.Spec.SecurityContext.RunAsGroup)
	assert.Equal(t, v1.PullIfNotPresent, dep.Spec.Template.Spec.Containers[1].ImagePullPolicy)
	assert.EqualValues(t, 8443, dep.Spec.Template.Spec.Containers[1].Ports[0].ContainerPort)
	assert.Equal(t, "webhook-https", dep.Spec.Template.Spec.Containers[1].Ports[0].Name)

	err = bc.cleanupResources(ctx, nb)
	require.NoError(t, err)

	verifyNVCFBackendDeleted := func(ct *assert.CollectT) {
		// Note: CRDs are cleaned via owner references when the NVCFBackend CRD is deleted by Helm,
		// not by cleanupResources, so we don't check for CRD deletion here.

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))
	}
	require.EventuallyWithT(t, verifyNVCFBackendDeleted, 60*time.Second, 5*time.Second)
}

func TestBackendK8sSyncAllFeatures(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	fakeExit := func(int) {}

	p := PatchOSExit(t, fakeExit)
	t.Cleanup(p.Unpatch)

	agentOpts := AgentOptions{
		KubeConfigPath:  "test/kubeconfig.yaml",
		SystemNamespace: NVCAOperatorNamespace,
		SvcAddress:      "localhost",
		AdminAddr:       "localhost",
	}
	_, err := NewAgent(ctx, &agentOpts)
	assert.NoError(t, err)

	agentOpts = AgentOptions{
		KubeConfigPath:     "test/kubeconfig.yaml",
		SystemNamespace:    NVCAOperatorNamespace,
		SvcAddress:         "localhost",
		AdminAddr:          "localhost",
		K8sVersionOverride: "1.25.8",
		TokenFetcher:       &mockTokenFetcher{token: "randomkey"},
	}
	ag, err := NewAgent(ctx, &agentOpts)
	require.NoError(t, err)

	// dummy backendk8s
	clients := mockKubeClients()
	b := NewBackendK8sCacheBuilder().
		WithSystemNamespace(agentOpts.SystemNamespace).
		WithK8sVersionOverride(agentOpts.K8sVersionOverride).
		WithNGCServiceKeyFetcher(agentOpts.TokenFetcher).
		WithClients(clients).
		WithGXCache(clustermgmt.DefaultGXCacheNamespace, true).
		WithAgentConfig(nvidiaiov1.AgentConfig{
			DeploymentConfig: nvidiaiov1.DeploymentConfig{
				PriorityClassName: agentOpts.PriorityClassName,
				NodeSelectorKey:   "nodename",
				NodeSelectorValue: "k8snode",
			},
		}).
		WithK8sClusterNetworkCIDRs([]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/12"}).
		WithNVCFWorkerConfig(true, types.DefaultCacheCSIMountOptions, 90*time.Minute)

	bc, _, err := b.Start(ctx)
	require.NoError(t, err)

	ag.backendk8scache = bc

	nb := getTestNVCFBackendAllFeatures()
	nb.Spec.Version = "2.45.0"
	nb.Spec.Overrides.Version = "2.45.0"

	_, err = bc.clients.NVCAOP.NvcfV1().NVCFBackends(agentOpts.SystemNamespace).Create(ctx, nb, metav1.CreateOptions{})
	require.NoError(t, err)

	var (
		svc *v1.Service
		dep *appsv1.Deployment
	)
	verifyNVCFBackendSynced := func(ct *assert.CollectT) {
		err = bc.SyncAllNVCFBackends(ctx)
		if !assert.NoError(ct, err) {
			return
		}

		_, err = bc.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, ICMSRequestCRDName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		_, err = bc.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, StorageRequestCRDName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		_, err = bc.clients.K8s.CoreV1().ResourceQuotas(getSystemNamespace(nb)).Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		ns, err := bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		assert.Contains(ct, ns.Labels, clustermgmt.ShaderCacheLabelKey)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, NetworkPoliciesConfigmapName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		// Vault is disabled, so configmap should not exist
		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, NVCAVaultConfigmapName, metav1.GetOptions{})
		if !assert.True(ct, k8serrors.IsNotFound(err)) {
			return
		}

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAImagePullSecretName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		// Vault is disabled, so both secrets should exist
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		_, err = bc.clients.K8s.RbacV1().ClusterRoles().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		_, err = bc.clients.K8s.RbacV1().ClusterRoleBindings().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, MiniServiceRBACConfigmapName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		_, err = bc.clients.K8s.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		_, err = bc.clients.K8s.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAWebhookTLSCertSecretName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAWebhookTLSCASecretName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NGCServiceAPIKeySecretName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OTELConfigSecretName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		svc, err = bc.clients.K8s.CoreV1().Services(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, nvcfCustomAnnotationsConfigMapName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		dep, err = bc.clients.K8s.AppsV1().Deployments(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}

		nbObj, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(agentOpts.SystemNamespace).Get(ctx, nb.Name, metav1.GetOptions{})
		if !assert.NoError(ct, err) {
			return
		}
		assert.Equal(ct, "2.45.0", nbObj.Spec.Version)
		assert.Equal(ct, nbObj.Spec.Overrides.Version, nbObj.Status.Version)
		assert.NotNil(ct, nbObj.Status.LastUpdated)
		assert.NotNil(ct, nbObj.Status.LastUpdatedAgentStatus)
	}
	require.EventuallyWithT(t, verifyNVCFBackendSynced, 60*time.Second, 5*time.Second)

	assert.Len(t, svc.Spec.Ports, 2)
	assert.EqualValues(t, 9000, svc.Spec.Ports[0].Port)
	assert.Equal(t, intstr.FromString("http"), svc.Spec.Ports[0].TargetPort)
	assert.EqualValues(t, 9003, svc.Spec.Ports[1].Port)
	assert.Equal(t, intstr.FromString("webhook-https"), svc.Spec.Ports[1].TargetPort)

	assert.Len(t, dep.Spec.Template.Spec.Containers, 2)
	assert.Equal(t, []string{"/usr/bin/nvca", "--config", "/var/run/nvca/config.yaml"}, dep.Spec.Template.Spec.Containers[0].Args)
	assert.Equal(t, []string{"/usr/bin/webhook-server", "--config", "/var/run/nvca/config.yaml"}, dep.Spec.Template.Spec.Containers[1].Args)
	assert.EqualValues(t, 9000, dep.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort)

	// Deployment should NOT have ImagePullSecrets set (must be nil/empty so pods use ServiceAccount secrets)
	assert.Empty(t, dep.Spec.Template.Spec.ImagePullSecrets, "Deployment ImagePullSecrets must be empty to use ServiceAccount secrets")

	// Check ServiceAccount ImagePullSecrets instead of Deployment PodSpec (secrets are now set on ServiceAccount)
	sa, err := bc.clients.K8s.CoreV1().ServiceAccounts(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Len(t, sa.ImagePullSecrets, 2)
	assert.Contains(t, sa.ImagePullSecrets, v1.LocalObjectReference{Name: NVCAImagePullSecretName})
	assert.Contains(t, sa.ImagePullSecrets, v1.LocalObjectReference{Name: "nvca-override-image-pull-secret"})

	assert.Equal(t, "http", dep.Spec.Template.Spec.Containers[0].Ports[0].Name)
	assert.Equal(t, NVCAWebhookContainerName, dep.Spec.Template.Spec.Containers[1].Name)
	assert.Equal(t, "local/webhook-server:latest", dep.Spec.Template.Spec.Containers[1].Image)
	assert.Equal(t, v1.PullNever, dep.Spec.Template.Spec.Containers[1].ImagePullPolicy)
	assert.EqualValues(t, 9002, dep.Spec.Template.Spec.Containers[1].Ports[0].ContainerPort)
	assert.Equal(t, "webhook-https", dep.Spec.Template.Spec.Containers[1].Ports[0].Name)

	err = bc.cleanupResources(ctx, nb)
	require.NoError(t, err)

	verifyNVCFBackendDeleted := func(ct *assert.CollectT) {
		// Note: CRDs are cleaned via owner references when the NVCFBackend CRD is deleted by Helm,
		// not by cleanupResources, so we don't check for CRD deletion here.

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))
	}
	require.EventuallyWithT(t, verifyNVCFBackendDeleted, 60*time.Second, 5*time.Second)
}

func TestBackendK8sSyncAllFeaturesCSIOptionOverride(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	fakeExit := func(int) {}

	p := PatchOSExit(t, fakeExit)
	t.Cleanup(p.Unpatch)

	agentOpts := AgentOptions{
		KubeConfigPath:  "test/kubeconfig.yaml",
		SystemNamespace: NVCAOperatorNamespace,
		SvcAddress:      "localhost",
		AdminAddr:       "localhost",
	}
	_, err := NewAgent(ctx, &agentOpts)
	assert.NoError(t, err)

	agentOpts = AgentOptions{
		KubeConfigPath:     "test/kubeconfig.yaml",
		SystemNamespace:    NVCAOperatorNamespace,
		SvcAddress:         "localhost",
		AdminAddr:          "localhost",
		K8sVersionOverride: "1.25.8",
		TokenFetcher:       &mockTokenFetcher{token: "randomkey"},
	}
	ag, err := NewAgent(ctx, &agentOpts)
	require.NoError(t, err)

	// dummy backendk8s
	clients := mockKubeClients()
	b := NewBackendK8sCacheBuilder().
		WithSystemNamespace(agentOpts.SystemNamespace).
		WithK8sVersionOverride(agentOpts.K8sVersionOverride).
		WithAgentConfig(nvidiaiov1.AgentConfig{
			DeploymentConfig: nvidiaiov1.DeploymentConfig{
				PriorityClassName: agentOpts.PriorityClassName,
				NodeSelectorKey:   "nodename",
				NodeSelectorValue: "k8snode",
			},
		}).
		WithNGCServiceKeyFetcher(agentOpts.TokenFetcher).
		WithClients(clients).
		WithGXCache(clustermgmt.DefaultGXCacheNamespace, true).
		WithK8sClusterNetworkCIDRs([]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/12"}).
		WithNVCFWorkerConfig(true, "remount,rw", 90*time.Minute)

	bc, _, err := b.Start(ctx)
	require.NoError(t, err)

	ag.backendk8scache = bc

	nb := getTestNVCFBackendAllFeaturesWithCSIOptionOverrides()
	nb.Spec.Version = "2.46.0"
	nb.Spec.Overrides.Version = "2.46.0"

	_, err = bc.clients.NVCAOP.NvcfV1().NVCFBackends(agentOpts.SystemNamespace).Create(ctx, nb, metav1.CreateOptions{})
	require.NoError(t, err)

	var (
		svc *v1.Service
		dep *appsv1.Deployment
	)
	verifyNVCFBackendSynced := func(ct *assert.CollectT) {
		err = bc.SyncAllNVCFBackends(ctx)
		require.NoError(ct, err)

		_, err = bc.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, ICMSRequestCRDName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, StorageRequestCRDName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ResourceQuotas(getSystemNamespace(nb)).Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.NoError(ct, err)

		ns, err := bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.NoError(ct, err)
		assert.Contains(t, ns.Labels, clustermgmt.ShaderCacheLabelKey)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, NetworkPoliciesConfigmapName, metav1.GetOptions{})
		require.NoError(ct, err)

		// Vault is disabled, so configmap should not exist
		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, NVCAVaultConfigmapName, metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAImagePullSecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		// Vault is disabled, so both secrets should exist
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.RbacV1().ClusterRoles().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.RbacV1().ClusterRoleBindings().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, MiniServiceRBACConfigmapName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAWebhookTLSCertSecretName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAWebhookTLSCASecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NGCServiceAPIKeySecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OTELConfigSecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		svc, err = bc.clients.K8s.CoreV1().Services(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		dep, err = bc.clients.K8s.AppsV1().Deployments(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		nbObj, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(agentOpts.SystemNamespace).Get(ctx, nb.Name, metav1.GetOptions{})
		require.NoError(ct, err)
		require.Equal(ct, "2.46.0", nbObj.Status.Version)
		require.NotNil(ct, nbObj.Status.LastUpdated)
		require.NotNil(ct, nbObj.Status.LastUpdatedAgentStatus)
	}
	require.EventuallyWithT(t, verifyNVCFBackendSynced, 60*time.Second, 5*time.Second)

	assert.Len(t, svc.Spec.Ports, 2)
	assert.EqualValues(t, 9000, svc.Spec.Ports[0].Port)
	assert.Equal(t, intstr.FromString("http"), svc.Spec.Ports[0].TargetPort)
	assert.EqualValues(t, 9003, svc.Spec.Ports[1].Port)
	assert.Equal(t, intstr.FromString("webhook-https"), svc.Spec.Ports[1].TargetPort)

	assert.Len(t, dep.Spec.Template.Spec.Containers, 2)
	assert.Equal(t, []string{"/usr/bin/nvca", "--config", "/var/run/nvca/config.yaml"}, dep.Spec.Template.Spec.Containers[0].Args)
	assert.Equal(t, []string{"/usr/bin/webhook-server", "--config", "/var/run/nvca/config.yaml"}, dep.Spec.Template.Spec.Containers[1].Args)
	assert.EqualValues(t, 9000, dep.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort)

	// Deployment should NOT have ImagePullSecrets set (must be nil/empty so pods use ServiceAccount secrets)
	assert.Empty(t, dep.Spec.Template.Spec.ImagePullSecrets, "Deployment ImagePullSecrets must be empty to use ServiceAccount secrets")

	// Check ServiceAccount ImagePullSecrets instead of Deployment PodSpec (secrets are now set on ServiceAccount)
	sa, err := bc.clients.K8s.CoreV1().ServiceAccounts(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Len(t, sa.ImagePullSecrets, 2)
	assert.Contains(t, sa.ImagePullSecrets, v1.LocalObjectReference{Name: NVCAImagePullSecretName})
	assert.Contains(t, sa.ImagePullSecrets, v1.LocalObjectReference{Name: "nvca-override-image-pull-secret"})

	assert.Equal(t, "http", dep.Spec.Template.Spec.Containers[0].Ports[0].Name)
	assert.Equal(t, NVCAWebhookContainerName, dep.Spec.Template.Spec.Containers[1].Name)
	assert.Equal(t, "local/webhook-server:latest", dep.Spec.Template.Spec.Containers[1].Image)
	assert.Equal(t, v1.PullNever, dep.Spec.Template.Spec.Containers[1].ImagePullPolicy)
	assert.EqualValues(t, 9002, dep.Spec.Template.Spec.Containers[1].Ports[0].ContainerPort)
	assert.Equal(t, "webhook-https", dep.Spec.Template.Spec.Containers[1].Ports[0].Name)

	err = bc.cleanupResources(ctx, nb)
	require.NoError(t, err)

	verifyNVCFBackendDeleted := func(ct *assert.CollectT) {
		// Note: CRDs are cleaned via owner references when the NVCFBackend CRD is deleted by Helm,
		// not by cleanupResources, so we don't check for CRD deletion here.

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))
	}
	require.EventuallyWithT(t, verifyNVCFBackendDeleted, 60*time.Second, 5*time.Second)
}

func TestBackendK8sSyncAllFeaturesCSIOptionDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	fakeExit := func(int) {}

	p := PatchOSExit(t, fakeExit)
	t.Cleanup(p.Unpatch)

	agentOpts := AgentOptions{
		KubeConfigPath:  "test/kubeconfig.yaml",
		SystemNamespace: NVCAOperatorNamespace,
		SvcAddress:      "localhost",
		AdminAddr:       "localhost",
	}
	_, err := NewAgent(ctx, &agentOpts)
	assert.NoError(t, err)

	agentOpts = AgentOptions{
		KubeConfigPath:     "test/kubeconfig.yaml",
		SystemNamespace:    NVCAOperatorNamespace,
		SvcAddress:         "localhost",
		AdminAddr:          "localhost",
		K8sVersionOverride: "1.25.8",
		TokenFetcher:       &mockTokenFetcher{token: "randomkey"},
	}
	ag, err := NewAgent(ctx, &agentOpts)
	require.NoError(t, err)

	// dummy backendk8s
	clients := mockKubeClients()
	b := NewBackendK8sCacheBuilder().
		WithSystemNamespace(agentOpts.SystemNamespace).
		WithK8sVersionOverride(agentOpts.K8sVersionOverride).
		WithAgentConfig(nvidiaiov1.AgentConfig{
			DeploymentConfig: nvidiaiov1.DeploymentConfig{
				PriorityClassName: agentOpts.PriorityClassName,
				NodeSelectorKey:   "nodename",
				NodeSelectorValue: "k8snode",
			},
		}).
		WithNGCServiceKeyFetcher(ag.TokenFetcher).
		WithNGCServiceKeyFetcher(agentOpts.TokenFetcher).
		WithClients(clients).
		WithGXCache(clustermgmt.DefaultGXCacheNamespace, true).
		WithK8sClusterNetworkCIDRs([]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/12"})

	bc, _, err := b.Start(ctx)
	require.NoError(t, err)

	ag.backendk8scache = bc

	nb := getTestNVCFBackendAllFeaturesWithCSIMountDisabled()

	_, err = bc.clients.NVCAOP.NvcfV1().NVCFBackends(agentOpts.SystemNamespace).Create(ctx, nb, metav1.CreateOptions{})
	require.NoError(t, err)

	var (
		svc *v1.Service
		dep *appsv1.Deployment
	)
	verifyNVCFBackendSynced := func(ct *assert.CollectT) {
		err = bc.SyncAllNVCFBackends(ctx)
		require.NoError(ct, err)

		_, err = bc.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, ICMSRequestCRDName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, StorageRequestCRDName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ResourceQuotas(getSystemNamespace(nb)).Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.NoError(ct, err)

		ns, err := bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.NoError(ct, err)
		assert.Contains(t, ns.Labels, clustermgmt.ShaderCacheLabelKey)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, NetworkPoliciesConfigmapName, metav1.GetOptions{})
		require.NoError(ct, err)

		// Vault is disabled, so configmap should not exist
		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, NVCAVaultConfigmapName, metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAImagePullSecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		// Vault is disabled, so both secrets should exist
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.RbacV1().ClusterRoles().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.RbacV1().ClusterRoleBindings().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, MiniServiceRBACConfigmapName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAWebhookTLSCertSecretName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAWebhookTLSCASecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NGCServiceAPIKeySecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OTELConfigSecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		svc, err = bc.clients.K8s.CoreV1().Services(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		dep, err = bc.clients.K8s.AppsV1().Deployments(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		nbObj, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(agentOpts.SystemNamespace).Get(ctx, nb.Name, metav1.GetOptions{})
		require.NoError(ct, err)
		require.Equal(ct, "2.45.0", nbObj.Spec.Version)
		require.Equal(ct, nbObj.Spec.Overrides.Version, nbObj.Status.Version)
		require.NotNil(ct, nbObj.Status.LastUpdated)
		require.NotNil(ct, nbObj.Status.LastUpdatedAgentStatus)
	}
	require.EventuallyWithT(t, verifyNVCFBackendSynced, 60*time.Second, 5*time.Second)

	assert.Len(t, svc.Spec.Ports, 2)
	assert.EqualValues(t, 9000, svc.Spec.Ports[0].Port)
	assert.Equal(t, intstr.FromString("http"), svc.Spec.Ports[0].TargetPort)
	assert.EqualValues(t, 9003, svc.Spec.Ports[1].Port)
	assert.Equal(t, intstr.FromString("webhook-https"), svc.Spec.Ports[1].TargetPort)

	assert.Len(t, dep.Spec.Template.Spec.Containers, 2)
	assert.Equal(t, []string{"/usr/bin/nvca", "--config", "/var/run/nvca/config.yaml"}, dep.Spec.Template.Spec.Containers[0].Args)
	assert.Equal(t, []string{"/usr/bin/webhook-server", "--config", "/var/run/nvca/config.yaml"}, dep.Spec.Template.Spec.Containers[1].Args)
	assert.EqualValues(t, 9000, dep.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort)

	// Deployment should NOT have ImagePullSecrets set (must be nil/empty so pods use ServiceAccount secrets)
	assert.Empty(t, dep.Spec.Template.Spec.ImagePullSecrets, "Deployment ImagePullSecrets must be empty to use ServiceAccount secrets")

	// Check ServiceAccount ImagePullSecrets instead of Deployment PodSpec (secrets are now set on ServiceAccount)
	sa, err := bc.clients.K8s.CoreV1().ServiceAccounts(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Len(t, sa.ImagePullSecrets, 2)
	assert.Contains(t, sa.ImagePullSecrets, v1.LocalObjectReference{Name: NVCAImagePullSecretName})
	assert.Contains(t, sa.ImagePullSecrets, v1.LocalObjectReference{Name: "nvca-override-image-pull-secret"})

	assert.Equal(t, "http", dep.Spec.Template.Spec.Containers[0].Ports[0].Name)
	assert.Equal(t, NVCAWebhookContainerName, dep.Spec.Template.Spec.Containers[1].Name)
	assert.Equal(t, "local/webhook-server:latest", dep.Spec.Template.Spec.Containers[1].Image)
	assert.Equal(t, v1.PullNever, dep.Spec.Template.Spec.Containers[1].ImagePullPolicy)
	assert.EqualValues(t, 9002, dep.Spec.Template.Spec.Containers[1].Ports[0].ContainerPort)
	assert.Equal(t, "webhook-https", dep.Spec.Template.Spec.Containers[1].Ports[0].Name)

	err = bc.cleanupResources(ctx, nb)
	require.NoError(t, err)

	verifyNVCFBackendDeleted := func(ct *assert.CollectT) {
		// Note: CRDs are cleaned via owner references when the NVCFBackend CRD is deleted by Helm,
		// not by cleanupResources, so we don't check for CRD deletion here.

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))
	}
	require.EventuallyWithT(t, verifyNVCFBackendDeleted, 60*time.Second, 5*time.Second)
}

func TestBackendBCPSyncAllFeatures(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	fakeExit := func(int) {}

	p := PatchOSExit(t, fakeExit)
	t.Cleanup(p.Unpatch)

	agentOpts := AgentOptions{
		KubeConfigPath:  "test/kubeconfig.yaml",
		SystemNamespace: NVCAOperatorNamespace,
		SvcAddress:      "localhost",
		AdminAddr:       "localhost",
	}
	_, err := NewAgent(ctx, &agentOpts)
	assert.NoError(t, err)

	agentOpts = AgentOptions{
		KubeConfigPath:     "test/kubeconfig.yaml",
		SystemNamespace:    NVCAOperatorNamespace,
		SvcAddress:         "localhost",
		AdminAddr:          "localhost",
		K8sVersionOverride: "1.25.8",
		TokenFetcher:       &mockTokenFetcher{token: "randomkey"},
	}
	ag, err := NewAgent(ctx, &agentOpts)
	require.NoError(t, err)

	// dummy backendk8s
	clients := mockKubeClients()
	b := NewBackendK8sCacheBuilder().
		WithSystemNamespace(agentOpts.SystemNamespace).
		WithK8sVersionOverride(agentOpts.K8sVersionOverride).
		WithAgentConfig(nvidiaiov1.AgentConfig{
			DeploymentConfig: nvidiaiov1.DeploymentConfig{
				PriorityClassName: agentOpts.PriorityClassName,
				NodeSelectorKey:   "nodename",
				NodeSelectorValue: "k8snode",
			},
		}).
		WithNGCServiceKeyFetcher(ag.TokenFetcher).
		WithNGCServiceKeyFetcher(agentOpts.TokenFetcher).
		WithClients(clients)

	bc, _, err := b.Start(ctx)
	require.NoError(t, err)

	ag.backendk8scache = bc

	nb := getTestNVCFBackendBCP()

	_, err = bc.clients.NVCAOP.NvcfV1().NVCFBackends(agentOpts.SystemNamespace).Create(ctx, nb, metav1.CreateOptions{})
	require.NoError(t, err)

	verifyNVCFBackendSynced := func(ct *assert.CollectT) {
		// First ensure the NVCFBackend is visible in the lister (informer has synced)
		backends, listErr := bc.nvcfBackendLister.List(labels.Everything())
		if !assert.NoError(ct, listErr) || !assert.NotEmpty(ct, backends, "NVCFBackend not yet visible in lister") {
			return
		}

		err = bc.SyncAllNVCFBackends(ctx)
		require.NoError(ct, err)

		_, err = bc.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, ICMSRequestCRDName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, StorageRequestCRDName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OAuthClientIDSecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, NetworkPoliciesConfigmapName, metav1.GetOptions{})
		require.NoError(ct, err)

		// Vault is disabled, so configmap should not exist
		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, NVCAVaultConfigmapName, metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAImagePullSecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		// Vault is disabled, so both secrets should exist
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OAuthClientKeySecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.RbacV1().ClusterRoles().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.RbacV1().ClusterRoleBindings().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		// NB: none of these should be created but there are no feature flags
		// in NVCA 2.0 to turn them off. Perhaps later these feature flags will be re-added.
		_, err = bc.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb)).Get(ctx, MiniServiceRBACConfigmapName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAWebhookTLSCertSecretName, metav1.GetOptions{})
		require.NoError(ct, err)
		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NVCAWebhookTLSCASecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, OTELConfigSecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nb)).Get(ctx, NGCServiceAPIKeySecretName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.CoreV1().Services(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		_, err = bc.clients.K8s.AppsV1().Deployments(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		require.NoError(ct, err)

		nbObj, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(agentOpts.SystemNamespace).Get(ctx, nb.Name, metav1.GetOptions{})
		require.NoError(ct, err)
		require.Equal(ct, "v1.26.6", nbObj.Spec.Version)
		require.Equal(ct, nbObj.Spec.Overrides.Version, nbObj.Status.Version)
		require.NotNil(ct, nbObj.Status.LastUpdated)
		require.NotNil(ct, nbObj.Status.LastUpdatedAgentStatus)
	}
	require.EventuallyWithT(t, verifyNVCFBackendSynced, 60*time.Second, 5*time.Second)

	err = bc.cleanupResources(ctx, nb)
	require.NoError(t, err)

	verifyNVCFBackendDeleted := func(ct *assert.CollectT) {
		// Note: CRDs are cleaned via owner references when the NVCFBackend CRD is deleted by Helm,
		// not by cleanupResources, so we don't check for CRD deletion here.

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getSystemNamespace(nb), metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))

		_, err = bc.clients.K8s.CoreV1().Namespaces().Get(ctx, getRequestsNamespace(nb), metav1.GetOptions{})
		require.True(ct, k8serrors.IsNotFound(err))
	}
	require.EventuallyWithT(t, verifyNVCFBackendDeleted, 60*time.Second, 5*time.Second)
}

func Test_shouldUpdateNVCFStatus(t *testing.T) {
	tests := []struct {
		inputNBStatus   nvidiaiov1.NVCFBackendStatus
		inputNVCAStatus nvcfBackendHealthResponse
		expectedResult  bool
	}{
		{
			inputNBStatus:   nvidiaiov1.NVCFBackendStatus{},
			inputNVCAStatus: nvcfBackendHealthResponse{},
			expectedResult:  false,
		},
		{
			inputNBStatus: nvidiaiov1.NVCFBackendStatus{
				AgentStatus:       nvidiaiov1.AgentStatusHealthy,
				KubernetesVersion: "1.0.0",
				GPUUsage: map[nvidiaiov1.GPUName]nvidiaiov1.GPUResource{
					"A100": {
						Capacity:  1,
						Available: 0,
						Allocated: 1,
					},
				},
			},
			inputNVCAStatus: nvcfBackendHealthResponse{
				Status:     nvidiaiov1.AgentStatusHealthy,
				K8sVersion: "1.0.0",
				GPUUsage: map[nvidiaiov1.GPUName]nvidiaiov1.GPUResource{
					"A100": {
						Capacity:  1,
						Available: 0,
						Allocated: 1,
					},
				},
			},
			expectedResult: false,
		},
		{
			inputNBStatus: nvidiaiov1.NVCFBackendStatus{
				AgentStatus:       nvidiaiov1.AgentStatusHealthy,
				KubernetesVersion: "1.0.1",
				GPUUsage: map[nvidiaiov1.GPUName]nvidiaiov1.GPUResource{
					"A100": {
						Capacity:  1,
						Available: 0,
						Allocated: 1,
					},
				},
			},
			inputNVCAStatus: nvcfBackendHealthResponse{
				Status:     nvidiaiov1.AgentStatusHealthy,
				K8sVersion: "1.0.0",
				GPUUsage: map[nvidiaiov1.GPUName]nvidiaiov1.GPUResource{
					"A100": {
						Capacity:  1,
						Available: 0,
						Allocated: 1,
					},
				},
			},
			expectedResult: true,
		},
		{
			inputNBStatus: nvidiaiov1.NVCFBackendStatus{
				AgentStatus:       nvidiaiov1.AgentStatusHealthy,
				KubernetesVersion: "1.0.0",
				GPUUsage: map[nvidiaiov1.GPUName]nvidiaiov1.GPUResource{
					"A100": {
						Capacity:  1,
						Available: 0,
						Allocated: 1,
					},
				},
			},
			inputNVCAStatus: nvcfBackendHealthResponse{
				Status:     nvidiaiov1.AgentStatusUnhealthy,
				K8sVersion: "1.0.0",
				GPUUsage: map[nvidiaiov1.GPUName]nvidiaiov1.GPUResource{
					"A100": {
						Capacity:  1,
						Available: 0,
						Allocated: 1,
					},
				},
			},
			expectedResult: true,
		},
		{
			inputNBStatus: nvidiaiov1.NVCFBackendStatus{
				AgentStatus:       nvidiaiov1.AgentStatusHealthy,
				KubernetesVersion: "1.0.0",
				GPUUsage: map[nvidiaiov1.GPUName]nvidiaiov1.GPUResource{
					"A100": {
						Capacity:  2,
						Available: 1,
						Allocated: 1,
					},
				},
			},
			inputNVCAStatus: nvcfBackendHealthResponse{
				Status:     nvidiaiov1.AgentStatusHealthy,
				K8sVersion: "1.0.0",
				GPUUsage: map[nvidiaiov1.GPUName]nvidiaiov1.GPUResource{
					"A100": {
						Capacity:  1,
						Available: 0,
						Allocated: 1,
					},
				},
			},
			expectedResult: true,
		},
	}

	for _, test := range tests {
		t.Run("", func(t *testing.T) {
			assert.Equal(t, test.expectedResult, shouldUpdateNVCFStatus(test.inputNBStatus, test.inputNVCAStatus))
		})
	}
}

func TestSyncNVCFBackendHealth(t *testing.T) {
	ctx := newTestContext()

	nvcaResponseBase := nvcfBackendHealthResponse{
		Status:     nvidiaiov1.AgentStatusHealthy,
		K8sVersion: "v1.27.0",
		GPUUsage: map[nvidiaiov1.GPUName]nvidiaiov1.GPUResource{
			"A100": {
				Capacity:  1,
				Available: 0,
				Allocated: 1,
			},
		},
	}
	nvcaResponse := nvcaResponseBase
	code := http.StatusOK
	nvcaServerHealthz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if code == http.StatusOK {
			json.NewEncoder(w).Encode(nvcaResponse)
		} else {
			w.WriteHeader(code)
		}
	}))
	t.Cleanup(nvcaServerHealthz.Close)
	tmpMakeHealthzURL := makeNVCAHealthzURL
	makeNVCAHealthzURL = func(nb *nvidiaiov1.NVCFBackend) (string, error) {
		return nvcaServerHealthz.URL, nil
	}
	t.Cleanup(func() { makeNVCAHealthzURL = tmpMakeHealthzURL })

	nb := &nvidiaiov1.NVCFBackend{}
	nbName := "test-backend"
	nb.Name = nbName
	nb.Namespace = NVCAOperatorNamespace

	dep := &appsv1.Deployment{}
	dep.Name = nvcaoptypes.NVCAModuleName
	dep.Namespace = getSystemNamespace(nb)
	dep.Status.Replicas = 1
	dep.Status.ReadyReplicas = 1

	eventRecorder := record.NewFakeRecorder(0)
	// Ignore events.
	eventRecorder.Events = nil
	bc := &BackendK8sCache{
		clients: &kubeclients.KubeClients{
			NVCAOP: fakenvcaopclient.NewSimpleClientset(nb),
			K8s:    fakek8sclient.NewSimpleClientset(dep),
		},
		httpClient:        nvcaServerHealthz.Client(),
		operatorNamespace: NVCAOperatorNamespace,
		eventRecorder:     eventRecorder,
	}

	// All resources exists and server returns healthy.
	err := bc.SyncNVCFBackendHealth(ctx, nb)
	require.NoError(t, err)
	var gotNB *nvidiaiov1.NVCFBackend
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotNB, err = bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace).Get(ctx, nbName, metav1.GetOptions{})
		if assert.NoError(ct, err) {
			assert.NotNil(ct, gotNB.Status.LastUpdatedAgentStatus)
		}
	}, 5*time.Second, 100*time.Millisecond)
	require.Equal(t, nvidiaiov1.AgentStatusHealthy, gotNB.Status.AgentStatus)
	require.Equal(t, nvcaResponse.K8sVersion, gotNB.Status.KubernetesVersion)
	require.Equal(t, nvcaResponse.GPUUsage, gotNB.Status.GPUUsage)
	lastStatus := gotNB.Status.LastUpdatedAgentStatus

	// Sync again, there should be no update.
	err = bc.SyncNVCFBackendHealth(ctx, gotNB)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	gotNB, err = bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace).Get(ctx, nbName, metav1.GetOptions{})
	require.NoError(t, err)
	require.True(t, gotNB.Status.LastUpdatedAgentStatus.Equal(lastStatus))

	// Bad code, unknown
	code = http.StatusNotFound
	err = bc.SyncNVCFBackendHealth(ctx, nb)
	require.NoError(t, err)
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotNB, err = bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace).Get(ctx, nbName, metav1.GetOptions{})
		if assert.NoError(ct, err) {
			assert.True(ct, gotNB.Status.LastUpdatedAgentStatus.After(lastStatus.Time))
		}
	}, 5*time.Second, 100*time.Millisecond)
	require.Equal(t, nvidiaiov1.AgentStatusUnknown, gotNB.Status.AgentStatus)
	require.Equal(t, "", gotNB.Status.KubernetesVersion)
	require.Nil(t, gotNB.Status.GPUUsage)
	lastStatus = gotNB.Status.LastUpdatedAgentStatus

	// Backend reports unhealthy
	code = http.StatusOK
	nvcaResponse = nvcfBackendHealthResponse{
		Status: nvidiaiov1.AgentStatusUnhealthy,
	}
	err = bc.SyncNVCFBackendHealth(ctx, nb)
	require.NoError(t, err)
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotNB, err = bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace).Get(ctx, nbName, metav1.GetOptions{})
		if assert.NoError(ct, err) {
			assert.True(ct, gotNB.Status.LastUpdatedAgentStatus.After(lastStatus.Time))
		}
	}, 5*time.Second, 100*time.Millisecond)
	require.Equal(t, nvidiaiov1.AgentStatusUnhealthy, gotNB.Status.AgentStatus)
	require.Equal(t, "", gotNB.Status.KubernetesVersion)
	require.Nil(t, gotNB.Status.GPUUsage)
	lastStatus = gotNB.Status.LastUpdatedAgentStatus

	// No status in response, unknown
	nvcaResponse = nvcaResponseBase
	nvcaResponse.Status = ""
	err = bc.SyncNVCFBackendHealth(ctx, nb)
	require.NoError(t, err)
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotNB, err = bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace).Get(ctx, nbName, metav1.GetOptions{})
		if assert.NoError(ct, err) {
			assert.True(ct, gotNB.Status.LastUpdatedAgentStatus.After(lastStatus.Time))
		}
	}, 5*time.Second, 100*time.Millisecond)
	require.Equal(t, nvidiaiov1.AgentStatusUnknown, gotNB.Status.AgentStatus)
	require.Equal(t, "", gotNB.Status.KubernetesVersion)
	require.Nil(t, gotNB.Status.GPUUsage)
	lastStatus = gotNB.Status.LastUpdatedAgentStatus

	// Too few ready replicas, unhealthy
	dep.Status.ReadyReplicas = 0
	_, err = bc.clients.K8s.AppsV1().Deployments(getSystemNamespace(nb)).UpdateStatus(ctx, dep, metav1.UpdateOptions{})
	require.NoError(t, err)
	nvcaResponse = nvcaResponseBase

	err = bc.SyncNVCFBackendHealth(ctx, nb)
	require.NoError(t, err)
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		gotNB, err = bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace).Get(ctx, nbName, metav1.GetOptions{})
		if assert.NoError(ct, err) {
			assert.True(ct, gotNB.Status.LastUpdatedAgentStatus.After(lastStatus.Time))
		}
	}, 5*time.Second, 100*time.Millisecond)
	require.Equal(t, nvidiaiov1.AgentStatusUnhealthy, gotNB.Status.AgentStatus)
	require.Equal(t, nvcaResponse.K8sVersion, gotNB.Status.KubernetesVersion)
	require.Equal(t, nvcaResponse.GPUUsage, gotNB.Status.GPUUsage)
	lastStatus = gotNB.Status.LastUpdatedAgentStatus
}

type PatchedOSExit struct {
	Called     bool
	CalledWith int
	patchFunc  *mpatch.Patch
}

func PatchOSExit(t *testing.T, mockOSExitImpl func(int)) *PatchedOSExit {
	patchedExit := &PatchedOSExit{Called: false}

	patchFunc, err := mpatch.PatchMethod(os.Exit, func(code int) {
		patchedExit.Called = true
		patchedExit.CalledWith = code

		mockOSExitImpl(code)
	})

	if err != nil {
		t.Errorf("Failed to patch os.Exit due to an error: %v", err)

		return nil
	}

	patchedExit.patchFunc = patchFunc

	return patchedExit
}

func (p *PatchedOSExit) Unpatch() {
	_ = p.patchFunc.Unpatch()
}

func TestOperator_shouldNVCARollout(t *testing.T) {
	lsTime := metav1.Time{Time: core.GetCurrentTime(newTestContext()).Add(-time.Minute * 10)}
	nb := &nvidiaiov1.NVCFBackend{
		Status: nvidiaiov1.NVCFBackendStatus{
			LastUpdated: &lsTime,
		},
	}

	// Version change
	nb.Spec.Version = "1.0.0"
	nb.Status.Version = "1.0.1"
	assert.True(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())
	nb.Status.Version = nb.Spec.Version
	assert.False(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())

	// AccountConfig
	nb = &nvidiaiov1.NVCFBackend{
		Status: nvidiaiov1.NVCFBackendStatus{
			LastUpdated: &lsTime,
		},
	}
	nb.Spec.AccountConfig = nvidiaiov1.AccountConfig{
		NCAID: "1234",
	}
	nb.Spec.AccountConfig.DeepCopyInto(&nb.Status.AccountConfig)
	assert.False(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())
	nb.Status.AccountConfig.NCAID = "4567"
	assert.True(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())

	// FeatureGate
	nb = &nvidiaiov1.NVCFBackend{
		Status: nvidiaiov1.NVCFBackendStatus{
			LastUpdated: &lsTime,
		},
	}
	nb.Spec.FeatureGate = nvidiaiov1.FeatureGate{
		OTELConfig: &nvidiaiov1.OTELConfig{
			ServiceName: "some-service",
		},
	}
	nb.Spec.FeatureGate.DeepCopyInto(&nb.Status.FeatureGate)
	assert.False(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())
	nb.Status.FeatureGate.OTELConfig.ServiceName = "other-service"
	assert.True(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())

	// ClusterConfig
	nb = &nvidiaiov1.NVCFBackend{
		Status: nvidiaiov1.NVCFBackendStatus{
			LastUpdated: &lsTime,
		},
	}

	// DeploymentConfig - Secret Mirror Config
	nb = &nvidiaiov1.NVCFBackend{
		Status: nvidiaiov1.NVCFBackendStatus{
			LastUpdated: &lsTime,
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AgentConfig: nvidiaiov1.AgentConfig{
					DeploymentConfig: nvidiaiov1.DeploymentConfig{
						SecretMirrorSourceNamespace: "old-namespace",
						SecretMirrorLabelSelector:   "app=old-app",
					},
				},
			},
		},
	}
	nb.Spec.AgentConfig.DeploymentConfig = nvidiaiov1.DeploymentConfig{
		SecretMirrorSourceNamespace: "new-namespace",
		SecretMirrorLabelSelector:   "app=new-app",
	}
	nb.Status.AgentConfig.DeploymentConfig = nb.Spec.AgentConfig.DeploymentConfig
	assert.False(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())
	nb.Spec.ClusterConfig = nvidiaiov1.ClusterConfig{
		ClusterID: "some-id-1",
	}
	nb.Spec.ClusterConfig.DeepCopyInto(&nb.Status.ClusterConfig)
	assert.False(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())
	nb.Status.ClusterConfig.ClusterID = "some-id-2"
	assert.True(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())

	// ICMSConfig
	nb = &nvidiaiov1.NVCFBackend{
		Status: nvidiaiov1.NVCFBackendStatus{
			LastUpdated: &lsTime,
		},
	}
	nb.Spec.ICMSConfig = nvidiaiov1.ICMSConfig{
		ICMSServiceURL: "http://some-service-local",
	}
	nb.Spec.ICMSConfig.DeepCopyInto(&nb.Status.ICMSConfig)
	assert.False(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())
	nb.Status.ICMSConfig.ICMSServiceURL = "http://some-other-service-local"
	assert.True(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())

	// OAuthConfig
	nb = &nvidiaiov1.NVCFBackend{
		Status: nvidiaiov1.NVCFBackendStatus{
			LastUpdated: &lsTime,
		},
	}
	nb.Spec.OAuthConfig = nvidiaiov1.OAuthConfig{
		TokenURL: "http://some-token-url",
	}
	nb.Spec.OAuthConfig.DeepCopyInto(&nb.Status.OAuthConfig)
	assert.False(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())
	nb.Status.OAuthConfig.TokenURL = "http://some-other-service-local"
	assert.True(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())

	// VaultConfig
	nb = &nvidiaiov1.NVCFBackend{
		Status: nvidiaiov1.NVCFBackendStatus{
			LastUpdated: &lsTime,
		},
	}
	nb.Spec.VaultConfig = nvidiaiov1.VaultConfig{
		VaultNamespace: "some-namespace",
	}
	nb.Spec.VaultConfig.DeepCopyInto(&nb.Status.VaultConfig)
	assert.False(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())
	nb.Status.VaultConfig.VaultNamespace = "some-other-namespace"
	assert.True(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())

	nb = &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			Overrides: &nvidiaiov1.NVCFBackendSpecT{
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					Repository: "https://random-repo",
					Tag:        "random-tag",
				},
			},
		},
		Status: nvidiaiov1.NVCFBackendStatus{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				NVCAImageConfig: nvidiaiov1.ImageConfig{
					Repository: "https://random-repo",
					Tag:        "random-tag",
				},
			},
			LastUpdated: &lsTime,
		},
	}
	// without merging it would rollout
	assert.True(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())

	// merge overrides
	mergeOverrides(nb)
	assert.False(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())

	nb = &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			Overrides: &nvidiaiov1.NVCFBackendSpecT{
				WebhookConfig: nvidiaiov1.WebhookConfig{
					ImageConfig: nvidiaiov1.ImageConfig{
						Repository: "https://random-repo",
						Tag:        "random-tag",
					},
				},
			},
		},
		Status: nvidiaiov1.NVCFBackendStatus{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				WebhookConfig: nvidiaiov1.WebhookConfig{
					ImageConfig: nvidiaiov1.ImageConfig{
						Repository: "https://random-repo",
						Tag:        "random-tag",
					},
				},
			},
			LastUpdated: &lsTime,
		},
	}
	// without merging it would rollout
	assert.True(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())

	// merge overrides
	mergeOverrides(nb)
	assert.False(t, hasNVCFBackendChangedCheck(newTestContext(), nb)())
}

func TestMakeNVCAHealthzURL(t *testing.T) {
	u, err := makeNVCAHealthzURL(&nvidiaiov1.NVCFBackend{})
	require.NoError(t, err)
	assert.Equal(t, "http://nvca.nvca-system:8000/healthz", u)

	u, err = makeNVCAHealthzURL(&nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				ClusterConfig: nvidiaiov1.ClusterConfig{
					SystemNamespace: "foo",
					SvcAddress:      ":9000",
				},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "http://nvca.foo:9000/healthz", u)

	u, err = makeNVCAHealthzURL(&nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				ClusterConfig: nvidiaiov1.ClusterConfig{
					SvcAddress: "blah",
				},
			},
		},
	})
	assert.ErrorContains(t, err, "parse NVCA service address:")

	u, err = makeNVCAHealthzURL(&nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				ClusterConfig: nvidiaiov1.ClusterConfig{
					SvcAddress: ":foo",
				},
			},
		},
	})
	assert.ErrorContains(t, err, "convert NVCA service port string:")
}

func TestWithNVCAImageRepo(t *testing.T) {
	ctx, cancel1 := context.WithCancel(context.Background())
	t.Cleanup(cancel1)
	agentOpts := AgentOptions{
		KubeConfigPath:     "test/kubeconfig.yaml",
		SystemNamespace:    NVCAOperatorNamespace,
		SvcAddress:         "localhost",
		AdminAddr:          "localhost",
		K8sVersionOverride: "1.25.8",
		TokenFetcher:       &mockTokenFetcher{token: "randomkey"},
		NVCAImageRepo:      "docker.io/some-nvca-image-repo/nvca",
	}
	_, err := NewAgent(ctx, &agentOpts)
	require.NoError(t, err)

	// dummy backendk8s without default
	clients := mockKubeClients()
	b := NewBackendK8sCacheBuilder().
		WithSystemNamespace(agentOpts.SystemNamespace).
		WithK8sVersionOverride(agentOpts.K8sVersionOverride).
		WithAgentConfig(nvidiaiov1.AgentConfig{
			DeploymentConfig: nvidiaiov1.DeploymentConfig{
				PriorityClassName: agentOpts.PriorityClassName,
				NodeSelectorKey:   "nodename",
				NodeSelectorValue: "k8snode",
			},
		}).
		WithNGCServiceKeyFetcher(agentOpts.TokenFetcher).
		WithNGCServiceKeyFetcher(agentOpts.TokenFetcher).
		WithNVCAImageRepo(agentOpts.NVCAImageRepo).
		WithClients(clients)

	bc, _, err := b.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, agentOpts.NVCAImageRepo, bc.nvcaImageRepo)
	assert.Equal(t, "docker.io/some-nvca-image-repo/nvca:1.0.0", bc.getWebhooksImagePathFromConfig(ctx, &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{Version: "1.0.0"}}}))
	assert.Equal(t, "docker.io/some-nvca-image-repo/nvca:1.0.0", bc.getNVCAImagePathFromConfig(
		&nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{Version: "1.0.0"}}},
	))
	cancel1()

	// dummy backendk8s with default
	b = NewBackendK8sCacheBuilder().
		WithSystemNamespace(agentOpts.SystemNamespace).
		WithK8sVersionOverride(agentOpts.K8sVersionOverride).
		WithAgentConfig(nvidiaiov1.AgentConfig{
			DeploymentConfig: nvidiaiov1.DeploymentConfig{
				PriorityClassName: agentOpts.PriorityClassName,
				NodeSelectorKey:   "nodename",
				NodeSelectorValue: "k8snode",
			},
		}).
		WithNGCServiceKeyFetcher(agentOpts.TokenFetcher).
		WithNGCServiceKeyFetcher(agentOpts.TokenFetcher).
		WithClients(clients)
	ctx, cancel2 := context.WithCancel(context.Background())
	t.Cleanup(cancel2)
	bc, _, err = b.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, "nvcr.io/nvidia/nvcf-byoc/nvca", bc.nvcaImageRepo)
	assert.Equal(t, "nvcr.io/nvidia/nvcf-byoc/nvca:1.0.0", bc.getWebhooksImagePathFromConfig(ctx,
		&nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{Version: "1.0.0"}}},
	))
	assert.Equal(t, "nvcr.io/nvidia/nvcf-byoc/nvca:1.0.0", bc.getNVCAImagePathFromConfig(
		&nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{Version: "1.0.0"}}},
	))
	cancel2()
}

func TestOperator_mergeOverrides(t *testing.T) {
	tests := []struct {
		name     string
		spec     nvidiaiov1.NVCFBackendSpecT
		override nvidiaiov1.NVCFBackendSpecT
		want     nvidiaiov1.NVCFBackendSpecT
	}{
		{
			name: "no overrides",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureA", "FeatureB"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{},
			want: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureA", "FeatureB"},
				},
			},
		},
		{
			name: "override replaces feature gates (non-maintenance modes)",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureA", "FeatureB"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureC", "FeatureD"},
				},
			},
			want: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					// Override replaces spec for non-maintenance features
					Values: []string{"FeatureC", "FeatureD"},
				},
			},
		},
		{
			name: "override with duplicate features",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureA", "FeatureB", "FeatureC"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureB", "FeatureC", "FeatureD"},
				},
			},
			want: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					// Override replaces spec, deduplicates within override
					Values: []string{"FeatureB", "FeatureC", "FeatureD"},
				},
			},
		},
		{
			name: "override other fields while replacing features",
			spec: nvidiaiov1.NVCFBackendSpecT{
				Version: "1.0.0",
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureA", "FeatureB"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.0.0",
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureC"},
				},
			},
			want: nvidiaiov1.NVCFBackendSpecT{
				Version: "2.0.0",
				FeatureGate: nvidiaiov1.FeatureGate{
					// Version is overridden, feature gates are replaced (not additive)
					Values: []string{"FeatureC"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nb := &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: tt.spec,
					Overrides:        &tt.override,
				},
			}

			err := mergeOverrides(nb)
			assert.NoError(t, err)

			// Sort the feature gate values for stable comparison
			sort.Strings(nb.Spec.FeatureGate.Values)
			sort.Strings(tt.want.FeatureGate.Values)

			assert.Equal(t, tt.want, nb.Spec.NVCFBackendSpecT)
		})
	}
}

func TestOperator_mergeOverrides_MaintenanceModes(t *testing.T) {
	tests := []struct {
		name                    string
		spec                    nvidiaiov1.NVCFBackendSpecT
		override                nvidiaiov1.NVCFBackendSpecT
		want                    nvidiaiov1.NVCFBackendSpecT
		expectMetricIncremented bool
	}{
		{
			name: "CordonMaintenance in spec only - kept",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureA"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureB"},
				},
			},
			want: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			expectMetricIncremented: false,
		},
		{
			name: "CordonAndDrainMaintenance in spec only - kept",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonAndDrainMaintenanceFeatureFlag, "FeatureA"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureB"},
				},
			},
			want: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonAndDrainMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			expectMetricIncremented: false,
		},
		{
			name: "CordonMaintenance in override only - kept",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureA"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			want: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			expectMetricIncremented: false,
		},
		{
			name: "CordonAndDrainMaintenance in override only - kept",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureA"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonAndDrainMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			want: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonAndDrainMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			expectMetricIncremented: false,
		},
		{
			name: "CordonMaintenance in spec and CordonAndDrainMaintenance in override - prefer CordonMaintenance",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureA"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonAndDrainMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			want: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			expectMetricIncremented: true,
		},
		{
			name: "CordonAndDrainMaintenance in spec and CordonMaintenance in override - prefer CordonMaintenance",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonAndDrainMaintenanceFeatureFlag, "FeatureA"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			want: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			expectMetricIncremented: true,
		},
		{
			name: "Both maintenance modes in spec only - prefer CordonMaintenance",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, cordonAndDrainMaintenanceFeatureFlag, "FeatureA"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureB"},
				},
			},
			want: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			expectMetricIncremented: true,
		},
		{
			name: "Both maintenance modes in override only - prefer CordonMaintenance",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureA"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, cordonAndDrainMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			want: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			expectMetricIncremented: true,
		},
		{
			name: "Both maintenance modes across spec and override - prefer CordonMaintenance",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, cordonAndDrainMaintenanceFeatureFlag, "FeatureA"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, cordonAndDrainMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			want: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureB"},
				},
			},
			expectMetricIncremented: true,
		},
		{
			name: "Override replaces other features but maintenance modes are additive",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureA", "FeatureB"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureC", "FeatureD"},
				},
			},
			want: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					// CordonMaintenance is kept (additive), but FeatureA/FeatureB are replaced by FeatureC/FeatureD
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureC", "FeatureD"},
				},
			},
			expectMetricIncremented: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nb := &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: tt.spec,
					Overrides:        &tt.override,
				},
			}

			// Note: We can't easily test the metric increment without setting up the full metrics context
			// but we verify the logic produces correct results
			err := mergeOverrides(nb)
			assert.NoError(t, err)

			// Sort the feature gate values for stable comparison
			sort.Strings(nb.Spec.FeatureGate.Values)
			sort.Strings(tt.want.FeatureGate.Values)

			assert.Equal(t, tt.want.FeatureGate.Values, nb.Spec.FeatureGate.Values,
				"Feature gate values should match expected result")
		})
	}
}

func TestOperator_mergeOverrides_OrderPreservation(t *testing.T) {
	tests := []struct {
		name              string
		spec              nvidiaiov1.NVCFBackendSpecT
		override          nvidiaiov1.NVCFBackendSpecT
		expectedOrder     []string
		concurrentRuns    int
		concurrentDelayMs int
	}{
		{
			name: "order preserved with simple feature list",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureZ", "FeatureA", "FeatureM", "FeatureB"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{},
				},
			},
			expectedOrder:     []string{"FeatureZ", "FeatureA", "FeatureM", "FeatureB"},
			concurrentRuns:    50,
			concurrentDelayMs: 1,
		},
		{
			name: "order preserved after override replacement",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureA", "FeatureB", "FeatureC"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureX", "FeatureY", "FeatureZ", "FeatureW"},
				},
			},
			expectedOrder:     []string{"FeatureX", "FeatureY", "FeatureZ", "FeatureW"},
			concurrentRuns:    50,
			concurrentDelayMs: 1,
		},
		{
			name: "order preserved with maintenance modes at end",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureZ", "FeatureA"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureX", "FeatureY"},
				},
			},
			expectedOrder:     []string{cordonMaintenanceFeatureFlag, "FeatureX", "FeatureY"},
			concurrentRuns:    50,
			concurrentDelayMs: 1,
		},
		{
			name: "order preserved with additive maintenance mode at end",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonMaintenanceFeatureFlag, "FeatureA"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"FeatureX", "FeatureY", "FeatureZ"},
				},
			},
			// CordonMaintenance should be appended at the end since it's not in override
			expectedOrder:     []string{"FeatureX", "FeatureY", "FeatureZ", cordonMaintenanceFeatureFlag},
			concurrentRuns:    50,
			concurrentDelayMs: 1,
		},
		{
			name: "order preserved with complex mixed features",
			spec: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{cordonAndDrainMaintenanceFeatureFlag, "FeatureOld1", "FeatureOld2"},
				},
			},
			override: nvidiaiov1.NVCFBackendSpecT{
				FeatureGate: nvidiaiov1.FeatureGate{
					Values: []string{"Feature1", "Feature2", "Feature3", "Feature4", "Feature5"},
				},
			},
			// CordonAndDrainMaintenance should be appended at the end
			expectedOrder:     []string{"Feature1", "Feature2", "Feature3", "Feature4", "Feature5", cordonAndDrainMaintenanceFeatureFlag},
			concurrentRuns:    100,
			concurrentDelayMs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run the merge operation concurrently to ensure order is preserved
			// even under concurrent access patterns
			done := make(chan bool)
			results := make(chan []string, tt.concurrentRuns)

			for i := 0; i < tt.concurrentRuns; i++ {
				go func(iteration int) {
					// Add a small delay to stagger goroutines
					if tt.concurrentDelayMs > 0 {
						time.Sleep(time.Duration(iteration%tt.concurrentDelayMs) * time.Millisecond)
					}

					// Create a fresh copy for each goroutine
					nb := &nvidiaiov1.NVCFBackend{
						Spec: nvidiaiov1.NVCFBackendSpec{
							NVCFBackendSpecT: tt.spec,
							Overrides:        &tt.override,
						},
					}

					err := mergeOverrides(nb)
					require.NoError(t, err)

					// Send the result
					results <- nb.Spec.FeatureGate.Values
					done <- true
				}(i)
			}

			// Wait for all goroutines to complete
			for i := 0; i < tt.concurrentRuns; i++ {
				<-done
			}
			close(results)

			// Verify all results have the same order
			for actualOrder := range results {
				assert.Equal(t, tt.expectedOrder, actualOrder,
					"Feature gate order should be preserved across concurrent merges")
			}
		})
	}
}

func TestCreateOrUpdateNVCFBackend(t *testing.T) {
	eventRecorder := record.NewFakeRecorder(0)
	// Ignore events.
	eventRecorder.Events = nil
	c := &BackendK8sCache{
		clients:              mockKubeClients(),
		operatorNamespace:    NVCAOperatorNamespace,
		eventRecorder:        eventRecorder,
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		now:                  time.Now,
		enableGXCache:        true,
	}

	ctx := newTestContext()
	var err error
	var gotNB, nbNew *nvidiaiov1.NVCFBackend
	var cm *corev1.ConfigMap
	nbClient := c.clients.NVCAOP.NvcfV1().NVCFBackends(c.operatorNamespace)

	// New backend without overrides.
	nb := getTestNVCFBackendAllFeatures()
	nb.Spec.Overrides = nil

	cmClient := c.clients.K8s.CoreV1().ConfigMaps(getSystemNamespace(nb))

	// Test create with the ResourceQuota in place to ensure the NVCFBackend is not created
	c.clients.K8s.CoreV1().ResourceQuotas(c.operatorNamespace).Create(ctx, &v1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name: nvcfBackendCountResourceQuotaName,
		},
	}, metav1.CreateOptions{})
	err = c.CreateOrUpdateNVCFBackend(ctx, nb)
	assert.NoError(t, err)
	_, err = c.clients.NVCAOP.NvcfV1().NVCFBackends(c.operatorNamespace).Get(ctx, nb.Name, metav1.GetOptions{})
	assert.Error(t, err)
	assert.True(t, k8serrors.IsNotFound(err))
	err = c.clients.K8s.CoreV1().ResourceQuotas(c.operatorNamespace).Delete(ctx, nvcfBackendCountResourceQuotaName, metav1.DeleteOptions{})
	assert.NoError(t, err)

	// Should create the backend.
	err = c.CreateOrUpdateNVCFBackend(ctx, nb)
	require.NoError(t, err)
	gotNB, err = nbClient.Get(ctx, nb.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, nb, gotNB)
	err = c.syncNVCFBackend(ctx, nb, false)
	require.NoError(t, err)
	cm, err = cmClient.Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	cfg, err := nvcaconfig.DecodeConfig([]byte(cm.Data[agentConfigFile]))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"CachingSupport", "GXCache", "LogPosting", "PeriodicInstanceStatusUpdate"}, cfg.Agent.FeatureFlags)

	// Set new field, should update the backend.
	nbNew = nb.DeepCopy()
	nbNew.Spec.FeatureGate.Values = []string{"Foo"}
	err = c.CreateOrUpdateNVCFBackend(ctx, nbNew)
	require.NoError(t, err)
	err = c.syncNVCFBackend(ctx, nbNew, false)
	require.NoError(t, err)
	cm, err = cmClient.Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	cfg, err = nvcaconfig.DecodeConfig([]byte(cm.Data[agentConfigFile]))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"Foo", "GXCache"}, cfg.Agent.FeatureFlags)

	// Set override, should update the backend after merge.
	nbOverrides := nb.DeepCopy()
	nbOverrides.Spec.Overrides = &nvidiaiov1.NVCFBackendSpecT{
		FeatureGate: nvidiaiov1.FeatureGate{
			Values: []string{"Bar"},
		},
	}
	_, err = nbClient.Update(ctx, nbOverrides, metav1.UpdateOptions{})
	require.NoError(t, err)

	nbNew = nb.DeepCopy()
	// Override replaces spec for non-maintenance mode features.
	// Since "Baz" is in spec and "Bar" is in override, only "Bar" should remain (override behavior)
	nbNew.Spec.FeatureGate.Values = []string{"Baz"}
	err = c.CreateOrUpdateNVCFBackend(ctx, nbNew)
	require.NoError(t, err)
	err = c.syncNVCFBackend(ctx, nbNew, false)
	require.NoError(t, err)
	cm, err = cmClient.Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	cfg, err = nvcaconfig.DecodeConfig([]byte(cm.Data[agentConfigFile]))
	require.NoError(t, err)
	// Bar from override replaces Baz from spec (override behavior for non-maintenance features)
	assert.ElementsMatch(t, []string{"Bar", "GXCache"}, cfg.Agent.FeatureFlags)

	// Set override, should update the backend after merge.
	nbOverrides = nb.DeepCopy()
	nbOverrides.Spec.Overrides = &nvidiaiov1.NVCFBackendSpecT{
		FeatureGate: nvidiaiov1.FeatureGate{
			Values: []string{cordonMaintenanceFeatureFlag},
		},
	}
	_, err = nbClient.Update(ctx, nbOverrides, metav1.UpdateOptions{})
	require.NoError(t, err)

	nbNew = nb.DeepCopy()
	// Override replaces spec for non-maintenance mode features.
	nbNew.Spec.FeatureGate.Values = []string{"Baz"}
	err = c.CreateOrUpdateNVCFBackend(ctx, nbNew)
	require.NoError(t, err)
	err = c.syncNVCFBackend(ctx, nbNew, false)
	require.NoError(t, err)
	cm, err = cmClient.Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	cfg, err = nvcaconfig.DecodeConfig([]byte(cm.Data[agentConfigFile]))
	require.NoError(t, err)
	// Bar from override replaces Baz from spec (override behavior for non-maintenance features)
	assert.ElementsMatch(t, []string{"CordonMaintenance", "GXCache"}, cfg.Agent.FeatureFlags)
}

func TestCreateOrUpdateNVCFBackend_PropagatesNVCAOTELConfig(t *testing.T) {
	eventRecorder := record.NewFakeRecorder(0)
	eventRecorder.Events = nil
	c := &BackendK8sCache{
		clients:              mockKubeClients(),
		operatorNamespace:    NVCAOperatorNamespace,
		eventRecorder:        eventRecorder,
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		now:                  time.Now,
		enableGXCache:        true,
		nvcaOTELConfig: &nvidiaiov1.OTELConfig{
			Exporter:    "lightstep",
			Endpoint:    "otel.test.nvidia.com:8282",
			ServiceName: "nvcf-nvca",
			AccessToken: "test-lightstep-token",
		},
	}

	ctx := newTestContext()
	nb := getTestNVCFBackendAllFeatures()
	nb.Spec.Overrides = nil
	nb.Spec.FeatureGate.OTELConfig = nil

	err := c.CreateOrUpdateNVCFBackend(ctx, nb)
	require.NoError(t, err)

	gotNB, err := c.clients.NVCAOP.NvcfV1().NVCFBackends(c.operatorNamespace).Get(ctx, nb.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, gotNB.Spec.FeatureGate.OTELConfig)
	assert.Equal(t, "lightstep", gotNB.Spec.FeatureGate.OTELConfig.Exporter)
	assert.Equal(t, "otel.test.nvidia.com:8282", gotNB.Spec.FeatureGate.OTELConfig.Endpoint)
	assert.Equal(t, "nvcf-nvca", gotNB.Spec.FeatureGate.OTELConfig.ServiceName)
	assert.Equal(t, "test-lightstep-token", gotNB.Spec.FeatureGate.OTELConfig.AccessToken)

	err = c.syncNVCFBackend(ctx, gotNB, false)
	require.NoError(t, err)

	secret, err := c.clients.K8s.CoreV1().Secrets(getSystemNamespace(gotNB)).Get(ctx, OTELConfigSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []byte("lightstep"), secret.Data[OTELExporterSecretKey])
	assert.Equal(t, []byte("nvcf-nvca"), secret.Data[OTELServiceNameKey])
	assert.Equal(t, []byte("test-lightstep-token"), secret.Data[OTELAccessTokenKey])
	assert.Equal(t, []byte("test-lightstep-token"), secret.Data["NVCA_AUTHZ_LIGHTSTEP_ACCESS_TOKEN"])
}

func TestCreateOrUpdateAndDeleteNVCFBackend(t *testing.T) {
	// Initialize test context
	ctx := context.Background()
	newBackend := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: "test-namespace",
		},
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				Version: "v1",
			},
		},
	}

	agentOpts := AgentOptions{
		KubeConfigPath:     "test/kubeconfig.yaml",
		SystemNamespace:    NVCAOperatorNamespace,
		SvcAddress:         "localhost",
		AdminAddr:          "localhost",
		K8sVersionOverride: "1.25.8",
		TokenFetcher:       &mockTokenFetcher{token: "randomkey"},
		NVCAImageRepo:      "docker.io/some-nvca-image-repo/nvca",
	}

	clients := mockKubeClients()
	b := NewBackendK8sCacheBuilder().
		WithSystemNamespace(agentOpts.SystemNamespace).
		WithK8sVersionOverride(agentOpts.K8sVersionOverride).
		WithAgentConfig(nvidiaiov1.AgentConfig{
			DeploymentConfig: nvidiaiov1.DeploymentConfig{
				PriorityClassName: agentOpts.PriorityClassName,
				NodeSelectorKey:   "nodename",
				NodeSelectorValue: "k8snode",
			},
		}).
		WithNGCServiceKeyFetcher(agentOpts.TokenFetcher).
		WithNVCAImageRepo(agentOpts.NVCAImageRepo).
		WithClients(clients)

	bc, _, _ := b.Start(ctx)

	err := bc.CreateOrUpdateNVCFBackend(ctx, newBackend)
	assert.NoError(t, err, "CreateOrUpdateNVCFBackend should not return an error")

	err = bc.DeleteNVCFBackend(ctx, newBackend)
	assert.NoError(t, err, "DeleteNVCFBackend should not return an error")
}

func TestGetNetworkPoliciesData(t *testing.T) {
	tests := []struct {
		name                   string
		k8sClusterNetworkCIDRs []string
		ddcsIPAllowList        []string
		wantErr                bool
		validate               func(*testing.T, map[string]string)
	}{
		{
			name:                   "default CIDRs",
			k8sClusterNetworkCIDRs: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/12"},
			validate: func(t *testing.T, netpols map[string]string) {
				assert.Contains(t, netpols[EgressNetworkPolicyNameKey], "10.0.0.0/8")
				assert.Contains(t, netpols[EgressNetworkPolicyNameKey], "172.16.0.0/12")
				assert.Contains(t, netpols[EgressNetworkPolicyNameKey], "192.168.0.0/16")
				assert.Contains(t, netpols[EgressNetworkPolicyNameKey], "100.64.0.0/12")
			},
		},
		{
			name:                   "custom CIDRs",
			k8sClusterNetworkCIDRs: []string{"192.168.1.0/24", "10.10.0.0/16"},
			validate: func(t *testing.T, netpols map[string]string) {
				assert.Contains(t, netpols[EgressNetworkPolicyNameKey], "192.168.1.0/24")
				assert.Contains(t, netpols[EgressNetworkPolicyNameKey], "10.10.0.0/16")
				assert.NotContains(t, netpols[EgressNetworkPolicyNameKey], "172.16.0.0/12")
			},
		},
		{
			name:                   "empty CIDRs",
			k8sClusterNetworkCIDRs: []string{},
			validate: func(t *testing.T, netpols map[string]string) {
				assert.NotContains(t, netpols[EgressNetworkPolicyNameKey], "10.0.0.0/8")
				assert.NotContains(t, netpols[EgressNetworkPolicyNameKey], "172.16.0.0/12")
			},
		},
		{
			name:                   "single CIDR",
			k8sClusterNetworkCIDRs: []string{"192.168.0.0/16"},
			validate: func(t *testing.T, netpols map[string]string) {
				assert.Contains(t, netpols[EgressNetworkPolicyNameKey], "192.168.0.0/16")
				assert.NotContains(t, netpols[EgressNetworkPolicyNameKey], "10.0.0.0/8")
				assert.NotContains(t, netpols[EgressNetworkPolicyNameKey], "172.16.0.0/12")
			},
		},
		{
			name:                   "with DDCS allow list",
			k8sClusterNetworkCIDRs: []string{"10.0.0.0/8"},
			ddcsIPAllowList:        []string{"1.2.3.4/32", "5.6.7.8/32"},
			validate: func(t *testing.T, netpols map[string]string) {
				assert.Contains(t, netpols[EgressNetworkPolicyNameKey], "10.0.0.0/8")
				assert.Contains(t, netpols[EgressNetworkPolicyNameKey], "1.2.3.4/32")
				assert.Contains(t, netpols[EgressNetworkPolicyNameKey], "5.6.7.8/32")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := core.WithDefaultLogger(context.Background())
			bc := &BackendK8sCache{
				k8sClusterNetworkCIDRs: tt.k8sClusterNetworkCIDRs,
				ddcsIPAllowList:        tt.ddcsIPAllowList,
			}
			nb := &nvidiaiov1.NVCFBackend{}

			netpols, err := bc.getNetworkPoliciesData(ctx, nb)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.NotNil(t, netpols)
			assert.Contains(t, netpols, EgressNetworkPolicyNameKey)
			assert.Contains(t, netpols, IngressNetworkPolicyNameKey)
			tt.validate(t, netpols)
		})
	}
}

func TestWithContainerResources(t *testing.T) {
	agentRR := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
	}
	webhookRR := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
	}
	otelCollectorRR := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
	}

	b := NewBackendK8sCacheBuilder().WithContainerResources(agentRR, webhookRR, otelCollectorRR)
	if b.agentResources.Limits.Cpu() == nil || b.webhookResources.Limits.Memory() == nil || b.otelCollectorResources.Limits.Cpu() == nil {
		t.Fatalf("resources not set by builder")
	}
}

func TestWithOTelCollectorConfig(t *testing.T) {
	tests := []struct {
		name    string
		repo    string
		tag     string
		enabled bool
	}{
		{
			name:    "custom OTel collector image enabled",
			repo:    "custom.registry.io/otel-collector",
			tag:     "1.0.0",
			enabled: true,
		},
		{
			name:    "default OTel collector image disabled",
			repo:    "nvcr.io/nvidia/nvcf-byoc/opentelemetry-collector-contrib",
			tag:     "0.139.0",
			enabled: false,
		},
		{
			name:    "empty values",
			repo:    "",
			tag:     "",
			enabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBackendK8sCacheBuilder().WithOTelCollectorConfig(tt.repo, tt.tag, tt.enabled)

			assert.Equal(t, tt.repo, b.otelCollectorImageRepo)
			assert.Equal(t, tt.tag, b.otelCollectorImageTag)
			assert.Equal(t, tt.enabled, b.otelCollectorEnabled)
		})
	}
}

func TestOTelCollectorConfigPropagation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	expectedRepo := "custom.registry.io/otel-collector"
	expectedTag := "v2.0.0"
	expectedEnabled := true

	clients := mockKubeClients()
	b := NewBackendK8sCacheBuilder().
		WithSystemNamespace(NVCAOperatorNamespace).
		WithNGCServiceKeyFetcher(&mockTokenFetcher{token: "randomkey"}).
		WithClients(clients).
		WithOTelCollectorConfig(expectedRepo, expectedTag, expectedEnabled)

	bc, _, err := b.Start(ctx)
	require.NoError(t, err)

	// Verify OTel collector config is propagated to BackendK8sCache
	assert.Equal(t, expectedRepo, bc.otelCollectorImageRepo)
	assert.Equal(t, expectedTag, bc.otelCollectorImageTag)
	assert.Equal(t, expectedEnabled, bc.otelCollectorEnabled)
}

func TestOTelCollectorResourcesPropagation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	expectedResources := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2000m"),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}

	clients := mockKubeClients()
	b := NewBackendK8sCacheBuilder().
		WithSystemNamespace(NVCAOperatorNamespace).
		WithNGCServiceKeyFetcher(&mockTokenFetcher{token: "randomkey"}).
		WithClients(clients).
		WithContainerResources(
			corev1.ResourceRequirements{}, // agent
			corev1.ResourceRequirements{}, // webhook
			expectedResources,             // otelCollector
		)

	bc, _, err := b.Start(ctx)
	require.NoError(t, err)

	// Verify OTel collector resources are propagated to BackendK8sCache
	assert.True(t, bc.otelCollectorResources.Limits.Cpu().Equal(expectedResources.Limits[corev1.ResourceCPU]))
	assert.True(t, bc.otelCollectorResources.Limits.Memory().Equal(expectedResources.Limits[corev1.ResourceMemory]))
	assert.True(t, bc.otelCollectorResources.Requests.Cpu().Equal(expectedResources.Requests[corev1.ResourceCPU]))
	assert.True(t, bc.otelCollectorResources.Requests.Memory().Equal(expectedResources.Requests[corev1.ResourceMemory]))
}

func TestValidateNVCFBackend(t *testing.T) {
	tests := []struct {
		name           string
		setupConfigMap func(clients *kubeclients.KubeClients) error
		expectedError  bool
		errorContains  string
	}{
		{
			name: "success when custom network policies configmap exists and is valid",
			setupConfigMap: func(clients *kubeclients.KubeClients) error {
				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      nvcfCustomNetworkPoliciesConfigMapName,
						Namespace: NVCAOperatorNamespace,
					},
					Data: map[string]string{
						"custom-policy-0": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: test-policy
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector: {}`,
					},
				}
				_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(context.Background(), cm, metav1.CreateOptions{})
				return err
			},
			expectedError: false,
		},
		{
			name: "success when custom network policies configmap doesn't exist",
			setupConfigMap: func(clients *kubeclients.KubeClients) error {
				// Don't create the configmap - it should not exist
				return nil
			},
			expectedError: false,
		},
		{
			name: "failure when custom network policy yaml is malformed",
			setupConfigMap: func(clients *kubeclients.KubeClients) error {
				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      nvcfCustomNetworkPoliciesConfigMapName,
						Namespace: NVCAOperatorNamespace,
					},
					Data: map[string]string{
						"malformed-policy": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: malformed-policy
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector: {}
      ports:
      - protocol: TCP
        port: 80
    invalid_yaml_structure: [`, // Malformed YAML
					},
				}
				_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(context.Background(), cm, metav1.CreateOptions{})
				return err
			},
			expectedError: true,
			errorContains: "failed to parse custom network policy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := core.WithDefaultLogger(context.Background())
			clients := mockKubeClients()

			// Setup the configmap if needed
			if tt.setupConfigMap != nil {
				err := tt.setupConfigMap(clients)
				require.NoError(t, err, "Failed to setup test configmap")
			}

			bc := &BackendK8sCache{
				clients:           clients,
				operatorNamespace: NVCAOperatorNamespace,
			}

			// Create a test NVCFBackend
			nb := &nvidiaiov1.NVCFBackend{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backend",
					Namespace: NVCAOperatorNamespace,
				},
			}

			err := bc.validateNVCFBackend(ctx, nb)

			if tt.expectedError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateNVCFBackend_ConfigMapRetrievalError(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clients := mockKubeClients()

	// Create a configmap with the right name but in a different namespace to simulate permission error
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcfCustomNetworkPoliciesConfigMapName,
			Namespace: "different-namespace", // Wrong namespace
		},
		Data: map[string]string{
			"test-policy": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: test-policy
spec:
  podSelector: {}`,
		},
	}
	_, err := clients.K8s.CoreV1().ConfigMaps("different-namespace").Create(ctx, cm, metav1.CreateOptions{})
	require.NoError(t, err)

	bc := &BackendK8sCache{
		clients:           clients,
		operatorNamespace: NVCAOperatorNamespace,
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: NVCAOperatorNamespace,
		},
	}

	// This should succeed because the configmap doesn't exist in the expected namespace
	// (it will be treated as NotFound)
	err = bc.validateNVCFBackend(ctx, nb)
	require.NoError(t, err)
}

func TestValidateNVCFBackend_EmptyPolicyValues(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clients := mockKubeClients()

	// Create a configmap with empty policy values (should be skipped)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcfCustomNetworkPoliciesConfigMapName,
			Namespace: NVCAOperatorNamespace,
		},
		Data: map[string]string{
			"empty-policy":      "",           // Empty string
			"whitespace-policy": "   \n  \t ", // Only whitespace
			"valid-policy": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: valid-policy
spec:
  podSelector: {}
  policyTypes:
  - Ingress`,
		},
	}
	_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, cm, metav1.CreateOptions{})
	require.NoError(t, err)

	bc := &BackendK8sCache{
		clients:           clients,
		operatorNamespace: NVCAOperatorNamespace,
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: NVCAOperatorNamespace,
		},
	}

	// This should succeed because empty policies are skipped and the valid one passes
	err = bc.validateNVCFBackend(ctx, nb)
	require.NoError(t, err)
}

func TestValidateNVCFBackend_MultipleValidationErrors(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clients := mockKubeClients()

	// Create a configmap with multiple policies, some malformed
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcfCustomNetworkPoliciesConfigMapName,
			Namespace: NVCAOperatorNamespace,
		},
		Data: map[string]string{
			"malformed-policy-1": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: malformed-policy-1
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  invalid_yaml: [`, // Malformed YAML
			"malformed-policy-2": `not-valid-yaml-at-all: {{{`, // Completely invalid YAML
			"valid-policy": `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: valid-policy
spec:
  podSelector: {}
  policyTypes:
  - Ingress`,
		},
	}
	_, err := clients.K8s.CoreV1().ConfigMaps(NVCAOperatorNamespace).Create(ctx, cm, metav1.CreateOptions{})
	require.NoError(t, err)

	bc := &BackendK8sCache{
		clients:           clients,
		operatorNamespace: NVCAOperatorNamespace,
	}

	nb := &nvidiaiov1.NVCFBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backend",
			Namespace: NVCAOperatorNamespace,
		},
	}

	err = bc.validateNVCFBackend(ctx, nb)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse custom network policy")
}

func TestWithEnvOverrides(t *testing.T) {
	builder := NewBackendK8sCacheBuilder()

	functionEnvB64 := "ZnVuY3Rpb25FbnY=" // base64 of "functionEnv"
	taskEnvB64 := "dGFza0Vudg=="         // base64 of "taskEnv"

	result := builder.WithEnvOverrides(functionEnvB64, taskEnvB64)

	assert.NotNil(t, result)
	assert.Equal(t, functionEnvB64, result.functionEnvOverridesB64)
	assert.Equal(t, taskEnvB64, result.taskEnvOverridesB64)
}

func TestSetGracefulShutdown(t *testing.T) {
	bc := &BackendK8sCache{}

	// Initially should be false
	assert.False(t, bc.IsGracefulShutdown())

	// Set to true
	bc.SetGracefulShutdown(true)
	assert.True(t, bc.IsGracefulShutdown())

	// Set back to false
	bc.SetGracefulShutdown(false)
	assert.False(t, bc.IsGracefulShutdown())
}

func TestIsGracefulShutdown(t *testing.T) {
	bc := &BackendK8sCache{}

	// Initially false
	assert.False(t, bc.IsGracefulShutdown())

	// After setting true
	bc.gracefulShutdown.Store(true)
	assert.True(t, bc.IsGracefulShutdown())
}

// TestCleanupResources_RemovesNVCFBackendFinalizer locks in the fix for
// direct `kubectl delete nvcfbackend ...` calls outside a helm uninstall flow.
// The operator's reconcile loop sees the CR's
// deletionTimestamp and runs cleanup, but historically never removed the
// `nvca-operator.finalizers.nvidia.io` finalizer afterward. The CR therefore
// stayed alive (held by its own finalizer), the next reconcile saw the same
// deletionTimestamp, ran cleanup again, and the operator looped forever
// logging "Successfully cleaned up resources". This test asserts that
// cleanupResources now removes the finalizer so the API server can GC the CR.
//
// Note: the fake nvcaop client does not honor finalizers (Delete hard-removes
// the resource), so we skip the Delete dance and simply pre-seed the CR with
// the operator finalizer present, which is the only state the production
// `RemoveNVCFBackendFinalizer` actually inspects.
func TestCleanupResources_RemovesNVCFBackendFinalizer(t *testing.T) {
	ctx := core.WithDefaultLogger(context.Background())
	clients := mockKubeClients()
	// The Create() below fires the shared informer's OnAdd handler, which
	// triggers a background syncNVCFBackend. That sync path calls
	// setupNGCServiceAPIKeySecret + setupImagePullSecrets, both of which
	// dereference bc.ngcServiceKeyFetcher. Wire a mock fetcher and disable
	// generateImagePullSecret so the auto-sync can complete (or no-op out)
	// without panicking — we only care about the explicit cleanupResources
	// call below, but the informer goroutine has to make forward progress
	// or the test crashes the whole package.
	bc, _, err := NewBackendK8sCacheBuilder().
		WithSystemNamespace(NVCAOperatorNamespace).
		WithGenerateImagePullSecret(false).
		WithNGCServiceKeyFetcher(&mockTokenFetcher{token: "test-token"}).
		WithClients(clients).
		Start(ctx)
	require.NoError(t, err)

	nb := getTestNVCFBackendMinimal()
	nb.Finalizers = []string{cleanup.NVCAOperatorFinalizer}
	created, err := clients.NVCAOP.NvcfV1().NVCFBackends(nb.Namespace).Create(ctx, nb, metav1.CreateOptions{})
	require.NoError(t, err)
	require.Contains(t, created.Finalizers, cleanup.NVCAOperatorFinalizer)

	err = bc.cleanupResources(ctx, created)
	require.NoError(t, err)

	after, err := clients.NVCAOP.NvcfV1().NVCFBackends(nb.Namespace).Get(ctx, nb.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, after.Finalizers, cleanup.NVCAOperatorFinalizer,
		"cleanupResources must strip the operator finalizer so K8s can GC the CR; "+
			"otherwise the reconciler loops forever on the same deletionTimestamp")
}
