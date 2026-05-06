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
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	k8sinformers "k8s.io/client-go/informers"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/icms"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	k8smock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil/mock"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcafake "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	fakenodefeatures "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/fake"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func init() {
	newDynamicClient = func(scheme *runtime.Scheme, _ *rest.Config) (dynamic.Interface, error) {
		return fakedynamic.NewSimpleDynamicClient(scheme), nil
	}
}

func TestK8sComputeBackendEnsureNetPolicy(t *testing.T) {
	ctx := newTestContext()

	npCM := k8smock.NewNetworkPolicyConfigMap(SystemNamespace)

	msNS := &v1.Namespace{
		Status: v1.NamespaceStatus{
			Phase: corev1.NamespaceActive,
		},
	}
	msNS.Name = "sr-ns-miniservice"
	msNS.Labels = labels.Set{
		"bartnamespace":                     "true",
		nvcatypes.WorkloadInstanceTypeLabel: nvcatypes.WorkloadInstanceTypeValueMiniService,
	}

	podNS := &v1.Namespace{
		Status: v1.NamespaceStatus{
			Phase: corev1.NamespaceActive,
		},
	}
	podNS.Name = RequestsNamespace
	podNS.Labels = labels.Set{
		"bartnamespace":                     "true",
		nvcatypes.WorkloadInstanceTypeLabel: nvcatypes.WorkloadInstanceTypeValuePodSpec,
	}

	clients := mockKubeClients()
	clients.K8s = fakek8sclient.NewSimpleClientset(
		bartBackendGPUsConfigMap(),
		npCM,
		msNS,
		podNS,
	)

	b, _, err := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{
			"bartnamespace": "true",
		}).
		WithClients(clients).
		Start(ctx)
	require.NoError(t, err)

	checkNS := func(t *testing.T, namespace, expLabelVal string, hasIntraEgressNP bool) {
		t.Run(fmt.Sprintf("%s:%s", namespace, expLabelVal), func(t *testing.T) {
			assert.EventuallyWithT(t, func(c *assert.CollectT) {
				egressNP, err := clients.K8s.NetworkingV1().NetworkPolicies(namespace).Get(ctx,
					k8sutil.AllPodsEgressNetworkPolicyName, metav1.GetOptions{})
				if assert.NoError(c, err) {
					assert.Equal(c, []netv1.PolicyType{"Egress"}, egressNP.Spec.PolicyTypes)
					if !assert.Len(c, egressNP.Spec.Egress, 1) {
						return
					}
					if !assert.Len(c, egressNP.Spec.Egress[0].To, 1) {
						return
					}
					peer1 := egressNP.Spec.Egress[0].To[0]
					assert.NotNil(c, peer1.IPBlock)
					assert.Equal(c, "0.0.0.0/0", peer1.IPBlock.CIDR)
				}

				// Check for the intra-namespace egress policy
				if hasIntraEgressNP {
					intraEgressNP, err := clients.K8s.NetworkingV1().NetworkPolicies(namespace).Get(ctx, k8sutil.AllowEgressIntraNamespaceNetworkPolicyName, metav1.GetOptions{})
					if assert.NoError(c, err) {
						assert.Equal(c, []netv1.PolicyType{"Egress"}, intraEgressNP.Spec.PolicyTypes)
						if !assert.Len(c, intraEgressNP.Spec.Egress, 1) {
							return
						}
						if !assert.Len(c, intraEgressNP.Spec.Egress[0].To, 1) {
							return
						}
						peer := intraEgressNP.Spec.Egress[0].To[0]
						assert.NotNil(c, peer.NamespaceSelector)
						assert.Equal(c, namespace, peer.NamespaceSelector.MatchLabels[k8sutil.K8sNameLabelKey])
					}
				}

				ingressNP, err := clients.K8s.NetworkingV1().NetworkPolicies(namespace).Get(ctx,
					k8sutil.MonitoringIngressNetworkPolicyName, metav1.GetOptions{})
				if assert.NoError(c, err) {
					assert.Equal(c, []netv1.PolicyType{"Ingress"}, ingressNP.Spec.PolicyTypes)
					if !assert.Len(c, ingressNP.Spec.Ingress, 2) {
						return
					}
					if !assert.Len(c, ingressNP.Spec.Ingress[0].From, 1) {
						return
					}
					peer1 := ingressNP.Spec.Ingress[0].From[0]
					if !assert.NotNil(c, peer1.NamespaceSelector) {
						return
					}
					assert.Equal(c, expLabelVal, peer1.NamespaceSelector.MatchLabels["foo"])
					peer2 := ingressNP.Spec.Ingress[1].From[0]
					if !assert.NotNil(c, peer2.NamespaceSelector) {
						return
					}
					assert.Equal(c, namespace, peer2.NamespaceSelector.MatchLabels[k8sutil.K8sNameLabelKey])
				}
			}, 5*time.Second, 200*time.Millisecond, "namespace: "+namespace)
		})
	}

	checkNS(t, b.podInstanceNamespace, "bar", false)
	checkNS(t, msNS.Name, "bar", true)

	// Update and make sure changes are propagated by the informer's event handler.
	b.ForceSync(ctx)
	npCM.Data[k8sutil.MonitoringIngressNetworkPolicyName] = `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ` + k8sutil.MonitoringIngressNetworkPolicyName + `
spec:
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          foo: baz # This was updated
`
	_, err = b.clients.K8s.CoreV1().ConfigMaps(b.systemNamespace).Update(ctx, npCM, metav1.UpdateOptions{})
	require.NoError(t, err)
	b.ForceSync(ctx)

	checkNS(t, b.podInstanceNamespace, "baz", false)
	checkNS(t, msNS.Name, "baz", true)
}

func newHelmInstanceRBACConfigMap() *v1.ConfigMap {
	cm := &v1.ConfigMap{}
	cm.Name = helmChartInstanceRBACConfigMapName
	cm.Namespace = SystemNamespace
	cm.Data = map[string]string{
		helmChartInstanceRoleName: `
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ` + cm.Name + `
rules:
- apiGroups: [""]
  apiVersions: ["v1"]
  resources: ["pods", "services", "secrets"]
  verbs: ["*"]
- apiGroups: ["apps"]
  apiVersions: ["v1"]
  resources: ["deployments"]
  verbs: ["*"]
- apiGroups: ["rbac.authorization.k8s.io"]
  apiVersions: ["v1"]
  resources: ["roles", "rolebindings"]
  verbs: ["*"]
`,
	}

	return cm
}

func TestGetErroredPodLogs(t *testing.T) {
	ctx := newTestContext()

	cb := K8sComputeBackend{
		clients: makeMockKubeClients(),
		bk8s: &BackendK8sCache{
			logPostingEnabled: false,
		},
	}

	pod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-1",
			Namespace: "default",
		},
		Status: v1.PodStatus{
			InitContainerStatuses: []v1.ContainerStatus{
				{
					Name: "good-init-container",
					State: v1.ContainerState{
						Terminated: &v1.ContainerStateTerminated{},
					},
				},
				{
					Name: "bad-init-container-exited",
					State: v1.ContainerState{
						Terminated: &v1.ContainerStateTerminated{
							ExitCode: 1,
						},
					},
				},
			},
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name: "good-container",
					State: v1.ContainerState{
						Running: &v1.ContainerStateRunning{},
					},
				},
				{
					Name: "waiting-container",
					State: v1.ContainerState{
						Waiting: &v1.ContainerStateWaiting{},
					},
				},
				{
					Name: "bad-container-exited",
					State: v1.ContainerState{
						Terminated: &v1.ContainerStateTerminated{
							ExitCode: 1,
						},
					},
				},
				{
					Name:         "bad-container-restart-loop",
					RestartCount: 1,
					State: v1.ContainerState{
						Running: &v1.ContainerStateRunning{},
					},
				},
			},
		},
	}
	_, err := cb.clients.K8s.CoreV1().Pods(pod1.Namespace).Create(ctx, pod1, metav1.CreateOptions{})
	require.NoError(t, err)

	gotPodLogsNoStream, gotWrittenNoStream, err := cb.GetErroredPodLogs(ctx, pod1, "", MaxBytesForPodLogs)
	assert.NoError(t, err)
	assert.EqualValues(t, 0, gotWrittenNoStream)
	assert.Equal(t, `INIT CONTAINER good-init-container EXITED, CODE=0
---
INIT CONTAINER bad-init-container-exited EXITED, CODE=1
---
CONTAINER good-container RUNNING
---
CONTAINER waiting-container WAITING
---
CONTAINER bad-container-exited EXITED, CODE=1
---
CONTAINER bad-container-restart-loop RUNNING:
(CONTAINER IN RESTART LOOP)
---
`, gotPodLogsNoStream)

	cb.bk8s.logPostingEnabled = true
	gotPodLogs, gotWritten, err := cb.GetErroredPodLogs(ctx, pod1, "pod terminated due to state image-pull-issues", MaxBytesForPodLogs)
	assert.NoError(t, err)
	assert.EqualValues(t, 72, gotWritten)
	assert.Equal(t, `pod terminated due to state image-pull-issues
---
INIT CONTAINER good-init-container EXITED, CODE=0
---
INIT CONTAINER bad-init-container-exited EXITED, CODE=1:
fake logs
---
CONTAINER good-container RUNNING
---
CONTAINER waiting-container WAITING
---
CONTAINER bad-container-exited EXITED, CODE=1:
fake logs
---
CONTAINER bad-container-restart-loop RUNNING:
(CONTAINER IN RESTART LOOP)
fake logs
---
`, gotPodLogs)

	maxBytes := int64(18)
	gotPodLogsShortened, gotWrittenShortened, err := cb.GetErroredPodLogs(ctx, pod1, "", maxBytes)
	assert.NoError(t, err)
	assert.EqualValues(t, maxBytes, gotWrittenShortened)
	assert.Equal(t, `INIT CONTAINER good-init-container EXITED, CODE=0
---
INIT CONTAINER bad-init-container-exited EXITED, CODE=1:
fake logs
---
CONTAINER good-container RUNNING
---
CONTAINER waiting-container WAITING
---
CONTAINER bad-container-exited EXITED, CODE=1:
fake logs
---
CONTAINER bad-container-restart-loop RUNNING:
(CONTAINER IN RESTART LOOP)
<pod log limit reached, see detailed logs>
---
`, gotPodLogsShortened)

	maxBytes = int64(0)
	gotPodLogsNone, gotWrittenNone, err := cb.GetErroredPodLogs(ctx, pod1, "", maxBytes)
	assert.NoError(t, err)
	assert.EqualValues(t, maxBytes, gotWrittenNone)
	assert.Equal(t, `INIT CONTAINER good-init-container EXITED, CODE=0
---
INIT CONTAINER bad-init-container-exited EXITED, CODE=1:
<pod log limit reached, see detailed logs>
---
CONTAINER good-container RUNNING
---
CONTAINER waiting-container WAITING
---
CONTAINER bad-container-exited EXITED, CODE=1:
<pod log limit reached, see detailed logs>
---
CONTAINER bad-container-restart-loop RUNNING:
(CONTAINER IN RESTART LOOP)
<pod log limit reached, see detailed logs>
---
`, gotPodLogsNone)

	pod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-2",
			Namespace: "default",
		},
		Status: v1.PodStatus{
			InitContainerStatuses: []v1.ContainerStatus{
				{
					Name: "init",
					State: v1.ContainerState{
						Terminated: &v1.ContainerStateTerminated{
							ExitCode: 1,
						},
					},
				},
			},
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name: "utils",
					State: v1.ContainerState{
						Terminated: &v1.ContainerStateTerminated{
							ExitCode: 1,
						},
					},
				},
			},
		},
	}
	_, err = cb.clients.K8s.CoreV1().Pods(pod2.Namespace).Create(ctx, pod2, metav1.CreateOptions{})
	require.NoError(t, err)

	gotPodLogsNoStream, gotWrittenNoStream, err = cb.GetErroredPodLogs(ctx, pod2, "", MaxBytesForPodLogs)
	assert.NoError(t, err)
	assert.EqualValues(t, 0, gotWrittenNoStream)
	assert.Equal(t, `INIT CONTAINER init EXITED, CODE=1:

---
CONTAINER utils EXITED, CODE=1:

---
`, gotPodLogsNoStream)

}

func Test_writePublicLogsOnly(t *testing.T) {
	type spec struct {
		name     string
		logStr   string
		expWrite bool
	}

	cases := []spec{
		{
			name:     "empty",
			logStr:   ``,
			expWrite: false,
		},
		{
			name:     "no public",
			logStr:   `{"ts":"123"}`,
			expWrite: false,
		},
		{
			name:     "public false",
			logStr:   `{"ts":"123","public":false}`,
			expWrite: false,
		},
		{
			name:     "public quoted false",
			logStr:   `{\"ts\":\"123\",\"public\":false}`,
			expWrite: false,
		},
		{
			name:     "public",
			logStr:   `{"ts":"123","public":true}`,
			expWrite: true,
		},
		{
			name:     "public quoted",
			logStr:   `{\"ts\":\"123\",\"public\":true}`,
			expWrite: true,
		},
		{
			name:     "public space quoted",
			logStr:   `{\"ts\":\"123\",\"public\": true}`,
			expWrite: true,
		},
		{
			name:     "public not json",
			logStr:   `"public":true`,
			expWrite: true,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			sb := strings.Builder{}
			lr := strings.NewReader(tt.logStr)
			gotN, gotErrs := writePublicLogsOnly(&sb, lr)
			if tt.expWrite {
				require.Equal(t, gotErrs, []error{nil})
				assert.Greater(t, gotN, int64(0))
			} else {
				require.Equal(t, gotErrs, []error(nil))
				assert.Equal(t, int64(0), gotN)
			}
		})
	}
}

func Test_ensureHelmResourceConstraints(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	req := &nvcav2beta1.ICMSRequest{}
	req.Name = "sr-foo"
	req.Spec.Action = common.FunctionCreationAction
	req.Spec.CreationMsgInfo.RequestedGPUCount = 1
	req.Spec.CreationMsgInfo.GPUType = "L40"
	req.Spec.CreationMsgInfo.InstanceType = "ON-PREM.GPU.L40"
	req.Spec.CreationMsgInfo.InstanceTypeName = "ON-PREM.GPU.L40_1x"
	req.Spec.CreationMsgInfo.InstanceTypeValue = "ON-PREM.GPU.L40"

	k8sClient := &kubeclients.KubeClients{
		K8s: fakek8sclient.NewSimpleClientset(
			&v1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: RequestsNamespace,
				},
			},
			&v1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "sr-foo",
				},
			},
		),
		BART: nvcafake.NewSimpleClientset(req),
	}

	f := k8sinformers.NewSharedInformerFactoryWithOptions(
		k8sClient.K8s,
		0,
	)

	pi := f.Core().V1().Pods()
	pinf := pi.Informer()
	plis := pi.Lister()

	f.Start(ctx.Done())

	cache.WaitForCacheSync(ctx.Done(), pinf.HasSynced)

	nfClient := &fakenodefeatures.Client{
		BackendGPUs: []types.BackendGPU{{
			Name: "L40",
			InstanceTypes: []types.InstanceType{{
				Name:         "ON-PREM.GPU.L40",
				CPU:          resource.MustParse("24"),
				SystemMemory: resource.MustParse("256Gi"),
				Storage:      resource.MustParse("1Ti"),
				GPUCount:     8,
			}},
		}},
	}
	mockFFF := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.HelmResourceConstraints,
		},
	}
	kc := K8sComputeBackend{
		clients: k8sClient,
		bk8s: &BackendK8sCache{
			podLister:           plis,
			nfClient:            nfClient,
			regITCache:          icms.NewRegistrationInstanceTypeCache(),
			featureFlagFetcher:  mockFFF,
			infraOverheadGetter: enforce.NoOpInfraOverheadGetter,
		},
	}

	kc.bk8s.regITCache.Put(types.BackendGPUs(nfClient.BackendGPUs).ToRegistration(false, corev1.ResourceList{}))

	var err error
	one := *resource.NewQuantity(1, resource.DecimalSI)
	two := *resource.NewQuantity(2, resource.DecimalSI)
	_ = two.String()
	three := *resource.NewQuantity(3, resource.DecimalSI)
	_ = three.String()
	six := *resource.NewQuantity(6, resource.DecimalSI)
	_ = six.String()

	sortRQs := func(items []v1.ResourceQuota) []v1.ResourceQuota {
		sort.Slice(items, func(i, j int) bool {
			return items[i].Name < items[j].Name
		})
		return items
	}
	rqClient := kc.clients.K8s.CoreV1().ResourceQuotas(RequestsNamespace)

	// The request should equal instance count.
	err = kc.ensureHelmResourceConstraints(ctx, RequestsNamespace, req)
	require.NoError(t, err)
	rqList, err := rqClient.List(ctx, metav1.ListOptions{})
	if assert.NoError(t, err) && assert.Len(t, sortRQs(rqList.Items), 1) {
		assert.Equal(t, []v1.ResourceQuota{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "max-gpus",
					Namespace: namespace,
				},
				Spec: v1.ResourceQuotaSpec{
					Hard: v1.ResourceList{
						"requests.nvidia.com/gpu": one,
						"limits.nvidia.com/gpu":   one,
					},
				},
			},
		}, rqList.Items)
	}

	// Set GPUs to 2, make sure quotas are updated.
	req.Spec.CreationMsgInfo.RequestedGPUCount = 2
	req.Spec.CreationMsgInfo.InstanceTypeName = "ON-PREM.GPU.L40_2x"

	err = kc.ensureHelmResourceConstraints(ctx, RequestsNamespace, req)
	require.NoError(t, err)
	rqList, err = rqClient.List(ctx, metav1.ListOptions{})
	if assert.NoError(t, err) && assert.Len(t, sortRQs(rqList.Items), 1) {
		assert.Equal(t, []v1.ResourceQuota{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "max-gpus",
					Namespace: namespace,
				},
				Spec: v1.ResourceQuotaSpec{
					Hard: v1.ResourceList{
						"requests.nvidia.com/gpu": two,
						"limits.nvidia.com/gpu":   two,
					},
				},
			},
		}, rqList.Items)
	}
}

func TestPodPhaseToAggregatedInstanceStatus(t *testing.T) {
	defaultTimeConfig := (&k8sutil.TimeConfig{
		WorkerDegradationTimeout: 30 * time.Minute,
		WorkerStartupTimeout:     time.Minute,
	}).Complete()

	// utils pod is not ready and should be pending
	aggStatus := podPhaseToAggregatedInstanceStatus(&v1.Pod{
		Status: v1.PodStatus{
			Phase: v1.PodRunning,
			Conditions: []v1.PodCondition{
				{
					Type:   v1.PodReady,
					Status: v1.ConditionFalse,
				},
			},
		},
	}, defaultTimeConfig, defaultTimeConfig.PodScheduledThreshold)
	assert.Equal(t, AggregatedInstanceStatusPending, aggStatus)

	// utils pod is ready and should be pending
	aggStatus = podPhaseToAggregatedInstanceStatus(&v1.Pod{
		Status: v1.PodStatus{
			Phase: v1.PodRunning,
			Conditions: []v1.PodCondition{
				{
					Type:   v1.PodReady,
					Status: v1.ConditionTrue,
				},
			},
		},
	}, defaultTimeConfig, defaultTimeConfig.PodScheduledThreshold)
	assert.Equal(t, AggregatedInstanceStatusSucceeded, aggStatus)
}

var (
	//go:embed testdata/creationmsg_function_container_launchspec.json
	creationgMsgFunctionContainerLaunchSpec string
	//go:embed testdata/creationmsg_function_container_launchspec_byoo.json
	creationgMsgFunctionContainerLaunchSpecBYOO string
	//go:embed testdata/creationmsg_function_helmchart_launchspec.json
	creationgMsgFunctionHelmChartLaunchSpec string
	//go:embed testdata/creationmsg_function_helmchart_launchspec_byoo.json
	creationgMsgFunctionHelmLaunchSpecBYOO string
)

func Test_translateFunctionLaunchSpecification(t *testing.T) {
	ctx := newTestContext()

	// Set additional resource overhead to ensure it does not get added
	// to container function inference containers.
	t.Setenv("NVCF_ADDITIONAL_OVERHEAD_RESOURCES_B64", base64.StdEncoding.EncodeToString([]byte(`
{
	"cpu": "1",
	"memory": "2Gi",
	"ephemeral-storage": "5Gi"
}
`)))

	cmContainer := decodeCM(t, creationgMsgFunctionContainerLaunchSpec)
	cmHelmChart := decodeCM(t, creationgMsgFunctionHelmChartLaunchSpec)

	fff := &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{
			featureflag.UseFunctionTranslator,
			featureflag.HelmResourceConstraints,
			featureflag.EnforceContainerFunctionResourceLimits,
			featureflag.InfraResourceOverhead,
		},
	}

	nfClient := &fakenodefeatures.Client{
		BackendGPUs: []types.BackendGPU{{
			Name: types.GPUName("L40"),
			InstanceTypes: []types.InstanceType{{
				Name:         types.InstanceName("DGX-CLOUD.GPU.L40"),
				FullName:     "NVIDIA-L40",
				CPU:          resource.MustParse("24"),
				GPUCount:     2,
				SystemMemory: resource.MustParse("256Gi"),
				Storage:      resource.MustParse("128Gi"),
			}},
		}},
	}
	regITCache := icms.NewRegistrationInstanceTypeCache()
	regITCache.Put(types.BackendGPUs(nfClient.BackendGPUs).ToRegistration(false, corev1.ResourceList{}))

	mockClients := makeMockKubeClients()
	kc := K8sComputeBackend{
		scheme:  runtime.NewScheme(),
		clients: mockClients,
		bk8s: &BackendK8sCache{
			requestsNamespace:   RequestsNamespace,
			nfClient:            nfClient,
			clients:             mockClients,
			featureFlagFetcher:  fff,
			regITCache:          regITCache,
			infraOverheadGetter: enforce.NewInfraOverheadGetter(fff, nvcaconfig.Config{}, nil),
			eventRecorder:       record.NewFakeRecorder(100),
		},
	}
	require.NoError(t, v1.AddToScheme(kc.scheme))
	require.NoError(t, batchv1.AddToScheme(kc.scheme))

	// Container basic function.
	msgMetaContainer := cmContainer.CreationQueueMessageMetadata
	reqContainer := &nvcav2beta1.ICMSRequest{
		ObjectMeta: getICMSRequestObjectMeta(
			types.DeploymentInfo{
				RequestID:      msgMetaContainer.RequestID,
				MessageID:      "msgidcontainer",
				MessageBatchID: msgMetaContainer.MessageBatchID,
				NCAID:          msgMetaContainer.NCAID,
				GPUType:        msgMetaContainer.GPUType,
			},
		),
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID:       msgMetaContainer.RequestID,
			Action:          msgMetaContainer.Action,
			MessageReceipt:  "receiptcontainer",
			NCAId:           msgMetaContainer.NCAID,
			MessageBatchID:  msgMetaContainer.MessageBatchID,
			FunctionDetails: cmContainer.Details,
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: msgMetaContainer,
				GPUName:                      msgMetaContainer.GPUType,
				QueueURL:                     "fooqueue",
				FunctionLaunchSpecification:  cmContainer.LaunchSpecification,
			},
		},
	}
	reqContainer.Name = "sr-123"
	reqContainer.Namespace = "foo"

	gotArtifactsContainer, err := kc.translateFunctionLaunchSpecification(ctx, reqContainer)
	require.NoError(t, err)
	assert.Len(t, gotArtifactsContainer, 3)

	require.Equal(t, function.LaunchArtifactTypePod, gotArtifactsContainer[2].Type)
	gotPodArtifactBytes, err := base64.StdEncoding.DecodeString(gotArtifactsContainer[2].Specification)
	require.NoError(t, err)
	gotPod := &corev1.Pod{}
	err = json.Unmarshal(gotPodArtifactBytes, gotPod)
	require.NoError(t, err)
	if assert.Len(t, gotPod.Spec.Containers, 2) && assert.Equal(t, "inference", gotPod.Spec.Containers[0].Name) {
		assert.Equal(t, "24", gotPod.Spec.Containers[0].Resources.Limits.Cpu().String())
		assert.Equal(t, "256Gi", gotPod.Spec.Containers[0].Resources.Limits.Memory().String())
		assert.Equal(t, "128Gi", gotPod.Spec.Containers[0].Resources.Limits.StorageEphemeral().String())
	}

	// Helm chart basic function.
	msgMetaHelmChart := cmHelmChart.CreationQueueMessageMetadata
	reqHelmChart := &nvcav2beta1.ICMSRequest{
		ObjectMeta: getICMSRequestObjectMeta(
			types.DeploymentInfo{
				RequestID:      msgMetaHelmChart.RequestID,
				MessageID:      "msgidhelmchart",
				MessageBatchID: msgMetaHelmChart.MessageBatchID,
				NCAID:          msgMetaHelmChart.NCAID,
				GPUType:        msgMetaHelmChart.GPUType,
			},
		),
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID:       msgMetaHelmChart.RequestID,
			Action:          msgMetaHelmChart.Action,
			MessageReceipt:  "receipthelmchart",
			NCAId:           msgMetaHelmChart.NCAID,
			MessageBatchID:  msgMetaHelmChart.MessageBatchID,
			FunctionDetails: cmHelmChart.Details,
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: msgMetaHelmChart,
				GPUName:                      msgMetaHelmChart.GPUType,
				QueueURL:                     "fooqueue",
				FunctionLaunchSpecification:  cmHelmChart.LaunchSpecification,
			},
		},
	}
	reqHelmChart.Name = "sr-456"

	gotArtifactsHelmChart, err := kc.translateFunctionLaunchSpecification(ctx, reqHelmChart)
	require.NoError(t, err)
	assert.Len(t, gotArtifactsHelmChart, 6)

	// For coverage
	_, err = kc.clients.BART.NvcaV2beta1().ICMSRequests(reqContainer.Namespace).Create(ctx, reqContainer, metav1.CreateOptions{})
	require.NoError(t, err)
	err = kc.ApplyCreationMessage(ctx, reqContainer)
	require.NoError(t, err)
	gotReq, err := kc.clients.BART.NvcaV2beta1().ICMSRequests(reqContainer.Namespace).Get(ctx, reqContainer.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Len(t, gotReq.Spec.CreationMsgInfo.LaunchArtifacts, 3)
}

func Test_parseHelmChartURL(t *testing.T) {
	type spec struct {
		name         string
		helmChartURL string
		expRepoURL   string
		expChartName string
		expVersion   string
		expError     string
	}

	cases := []spec{
		{
			name:         "basic",
			helmChartURL: "https://helm.ngc.nvidia.com/fakerepo/charts/foo-1.0.0.tgz",
			expRepoURL:   "https://helm.ngc.nvidia.com/fakerepo",
			expChartName: "foo",
			expVersion:   "1.0.0",
		},
		{
			name:         "repo subpaths",
			helmChartURL: "https://helm.ngc.nvidia.com/fakerepo/sub1/sub2/charts/foo-1.0.0.tgz",
			expRepoURL:   "https://helm.ngc.nvidia.com/fakerepo/sub1/sub2",
			expChartName: "foo",
			expVersion:   "1.0.0",
		},
		{
			name:         "chartname multi hyphen",
			helmChartURL: "https://helm.ngc.nvidia.com/fakerepo/sub1/sub2/charts/foo-bar-1.0.0.tgz",
			expRepoURL:   "https://helm.ngc.nvidia.com/fakerepo/sub1/sub2",
			expChartName: "foo-bar",
			expVersion:   "1.0.0",
		},
		{
			name:         "semver two versions",
			helmChartURL: "https://helm.ngc.nvidia.com/fakerepo/charts/foo-1-2-3-1.0.0.tgz",
			expRepoURL:   "https://helm.ngc.nvidia.com/fakerepo",
			expChartName: "foo-1-2-3",
			expVersion:   "1.0.0",
		},
		{
			name:         "semver v prefix",
			helmChartURL: "https://helm.ngc.nvidia.com/fakerepo/charts/foo-v1.0.0.tgz",
			expRepoURL:   "https://helm.ngc.nvidia.com/fakerepo",
			expChartName: "foo",
			expVersion:   "v1.0.0",
		},
		{
			name:         "semver short",
			helmChartURL: "https://helm.ngc.nvidia.com/fakerepo/charts/foo-1.0.tgz",
			expRepoURL:   "https://helm.ngc.nvidia.com/fakerepo",
			expChartName: "foo",
			expVersion:   "1.0",
		},
		{
			name:         "semver suffix plus",
			helmChartURL: "https://helm.ngc.nvidia.com/fakerepo/charts/foo-1.0.0+abc123.tgz",
			expRepoURL:   "https://helm.ngc.nvidia.com/fakerepo",
			expChartName: "foo",
			expVersion:   "1.0.0+abc123",
		},
		{
			name:         "semver suffix hyphen",
			helmChartURL: "https://helm.ngc.nvidia.com/fakerepo/charts/foo-1.0.0-abc123.tgz",
			expRepoURL:   "https://helm.ngc.nvidia.com/fakerepo",
			// Intentionally bad chart name and version.
			expChartName: "foo-1.0.0",
			expVersion:   "abc123",
		},
		{
			name:         "semver suffix hyphen plus",
			helmChartURL: "https://helm.ngc.nvidia.com/fakerepo/charts/foo-1.0.0-abc123+def456.tgz",
			expRepoURL:   "https://helm.ngc.nvidia.com/fakerepo",
			// Intentionally bad chart name and version.
			expChartName: "foo-1.0.0",
			expVersion:   "abc123+def456",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotRepoURL, gotChartName, gotVersion, err := parseHelmChartURL(tt.helmChartURL)
			if err != nil {
				assert.EqualError(t, err, tt.expError)
			} else {
				assert.Equal(t, tt.expRepoURL, gotRepoURL)
				assert.Equal(t, tt.expChartName, gotChartName)
				assert.Equal(t, tt.expVersion, gotVersion)
			}
		})
	}
}

func Test_isNamespaceConsideredTerminated(t *testing.T) {
	ctx := newTestContext()
	nsClient := mockKubeClients().K8s.CoreV1().Namespaces()
	namespaceName := "foo-ns"
	namespace := &v1.Namespace{}
	namespace.Name = namespaceName
	var isTerminated bool

	getNamespace := func(name string) (*v1.Namespace, error) {
		return nsClient.Get(ctx, name, metav1.GetOptions{})
	}
	defaultTimeConfig := (&k8sutil.TimeConfig{}).Complete()

	// Does not exist.
	isTerminated = isNamespaceConsideredTerminated(ctx, getNamespace, namespaceName, defaultTimeConfig)
	assert.True(t, isTerminated)

	// Exists.
	_, err := nsClient.Create(ctx, namespace, metav1.CreateOptions{})
	require.NoError(t, err)
	isTerminated = isNamespaceConsideredTerminated(ctx, getNamespace, namespaceName, defaultTimeConfig)
	assert.False(t, isTerminated)

	// Terminated but timestamp not after timeout.
	namespace.Status.Phase = corev1.NamespaceTerminating
	namespace.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	_, err = nsClient.Update(ctx, namespace, metav1.UpdateOptions{})
	require.NoError(t, err)
	isTerminated = isNamespaceConsideredTerminated(ctx, getNamespace, namespaceName, defaultTimeConfig)
	assert.False(t, isTerminated)

	// Terminated with stuck conditions.
	namespace.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	namespace.Status.Conditions = append(namespace.Status.Conditions, v1.NamespaceCondition{
		Type:   v1.NamespaceDeletionDiscoveryFailure,
		Status: v1.ConditionTrue,
	})
	_, err = nsClient.Update(ctx, namespace, metav1.UpdateOptions{})
	require.NoError(t, err)
	isTerminated = isNamespaceConsideredTerminated(ctx, getNamespace, namespaceName, defaultTimeConfig)
	assert.True(t, isTerminated)
}

func TestAddEnvsToUtilsContainers(t *testing.T) {
	tests := []struct {
		name     string
		pod      *v1.Pod
		envs     []v1.EnvVar
		expected []v1.EnvVar
	}{
		{
			name: "empty pod with no containers",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{},
				},
			},
			envs: []v1.EnvVar{
				{Name: "TEST_ENV", Value: "test_value"},
			},
			expected: nil,
		},
		{
			name: "pod with utils container",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: common.UtilsContainerName,
							Env: []v1.EnvVar{
								{Name: "EXISTING_ENV", Value: "existing_value"},
							},
						},
					},
				},
			},
			envs: []v1.EnvVar{
				{Name: "NEW_ENV", Value: "new_value"},
			},
			expected: []v1.EnvVar{
				{Name: "EXISTING_ENV", Value: "existing_value"},
				{Name: "NEW_ENV", Value: "new_value"},
			},
		},
		{
			name: "pod with multiple containers including utils",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "other-container",
							Env: []v1.EnvVar{
								{Name: "OTHER_ENV", Value: "other_value"},
							},
						},
						{
							Name: common.UtilsContainerName,
							Env:  []v1.EnvVar{},
						},
					},
				},
			},
			envs: []v1.EnvVar{
				{Name: "TEST_ENV1", Value: "test_value1"},
				{Name: "TEST_ENV2", Value: "test_value2"},
			},
			expected: []v1.EnvVar{
				{Name: "TEST_ENV1", Value: "test_value1"},
				{Name: "TEST_ENV2", Value: "test_value2"},
			},
		},
		{
			name: "pod without utils container",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "other-container",
							Env: []v1.EnvVar{
								{Name: "OTHER_ENV", Value: "other_value"},
							},
						},
					},
				},
			},
			envs: []v1.EnvVar{
				{Name: "TEST_ENV", Value: "test_value"},
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a copy of the pod to avoid modifying the test data
			pod := tt.pod.DeepCopy()

			// Call the function being tested
			addEnvsToUtilsContainers(pod, tt.envs...)

			// Find utils container if it exists
			var utilsContainer *v1.Container
			for i := range pod.Spec.Containers {
				if pod.Spec.Containers[i].Name == common.UtilsContainerName {
					utilsContainer = &pod.Spec.Containers[i]
					break
				}
			}

			if tt.expected == nil {
				// If we don't expect any changes
				if utilsContainer != nil {
					assert.Empty(t, utilsContainer.Env, "Utils container should not have env vars added")
				}
			} else {
				// Verify utils container exists and has correct env vars
				assert.NotNil(t, utilsContainer, "Utils container should exist")
				assert.ElementsMatch(t, tt.expected, utilsContainer.Env,
					"Environment variables don't match expected values")
			}
		})
	}
}

func Test_CheckTranslateLibShim_ContainerCache(t *testing.T) {
	origUUID := GetUseUUIDForRequestObjName()
	SetUseUUIDForRequestObjName(false)
	t.Cleanup(func() { SetUseUUIDForRequestObjName(origUUID) })
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)
	skipVolumeDetachCheck = true
	t.Cleanup(func() { skipVolumeDetachCheck = false })

	clients := makeMockKubeClients()
	nfClient := &fakenodefeatures.Client{
		BackendGPUs: []types.BackendGPU{{
			Name: types.GPUName("L40"),
			InstanceTypes: []types.InstanceType{{
				Name:         types.InstanceName("DGX-CLOUD.GPU.L40"),
				FullName:     "NVIDIA-L40",
				CPU:          *resource.NewQuantity(24, resource.DecimalSI),
				GPUCount:     2,
				SystemMemory: resource.MustParse("256Gi"),
			}},
		}},
	}
	regITCache := icms.NewRegistrationInstanceTypeCache()
	regITCache.Put(types.BackendGPUs(nfClient.BackendGPUs).ToRegistration(false, corev1.ResourceList{}))

	bc := &BackendK8sCache{
		requestsNamespace:    RequestsNamespace,
		podInstanceNamespace: RequestsNamespace,
		systemNamespace:      SystemNamespace,
		nfClient:             nfClient,
		// Turning on the function translation feature flag should result in no
		// launch artifacts populated initially.
		featureFlagFetcher: &featureflagmock.Fetcher{
			EnabledFFs: []*featureflag.FeatureFlag{featureflag.UseFunctionTranslator},
		},
		clients:                 clients,
		eventRecorder:           record.NewFakeRecorder(10),
		cachingSupportEnabled:   true,
		nvmeshEncryptionEnabled: true,
		regITCache:              regITCache,
		k8sTimeConfig: (&k8sutil.TimeConfig{
			ModelCacheVolumeDetachmentTimeout: 2 * time.Second,
		}).Complete(),
		imageCredentialHelperImage: "stg.nvcr.io/nv-cf/nvcf-core/image-credential-helper:latest",
		infraOverheadGetter:        enforce.NoOpInfraOverheadGetter,
	}
	kb := K8sComputeBackend{
		scheme:  runtime.NewScheme(),
		clients: clients,
		bk8s:    bc,
	}
	require.NoError(t, v1.AddToScheme(kb.scheme))
	require.NoError(t, batchv1.AddToScheme(kb.scheme))

	cmsgLS := function.CreationQueueMessage{
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			RequestID:         "randomRequestID1",
			NCAID:             "randomID",
			MessageBatchID:    "randomMessageBatchID",
			Action:            common.FunctionCreationAction,
			InstanceCount:     2,
			RequestedGPUCount: 1,
			InstanceType:      "DGX-CLOUD.GPU.L40",
			InstanceTypeName:  "DGX-CLOUD.GPU.L40_1x",
			InstanceTypeValue: "DGX-CLOUD.GPU.L40",
			GPUType:           "L40",
		},
		Details: function.Details{
			FunctionID:        "functionId1",
			FunctionVersionID: "functionVersionId1",
		},
	}
	req := &nvcav2beta1.ICMSRequest{}
	req.ObjectMeta = getICMSRequestObjectMeta(types.DeploymentInfo{
		RequestID:         cmsgLS.RequestID,
		MessageID:         "randomId1",
		MessageBatchID:    cmsgLS.MessageBatchID,
		NCAID:             cmsgLS.NCAID,
		GPUType:           cmsgLS.GPUType,
		FunctionID:        cmsgLS.Details.FunctionID,
		FunctionVersionID: cmsgLS.Details.FunctionVersionID,
	})
	req.Namespace = bc.requestsNamespace
	req.Spec = nvcav2beta1.ICMSRequestSpec{
		RequestID:      cmsgLS.RequestID,
		Action:         cmsgLS.Action,
		MessageReceipt: "randomMsgReceipt1",
		NCAId:          cmsgLS.NCAID,
		MessageBatchID: cmsgLS.MessageBatchID,
		CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
			CreationQueueMessageMetadata: cmsgLS.CreationQueueMessageMetadata,
			GPUName:                      cmsgLS.GPUType,
			QueueURL:                     "queueurl",
			FunctionLaunchSpecification: &function.LaunchSpecification{
				CloudProvider:   "DGXCLOUD",
				ICMSEnvironment: "prod",
				EnvironmentB64:  `RlVOQ1RJT05fSUQ9NWEzZDRhN2UtOWVlMy00NzYyLThkMzctZDNiNDBhNmY4NGM2CkZVTkNUSU9OX05BTUU9bXktZnVuYwpGVU5DVElPTl9WRVJTSU9OX0lEPTJjOTQ4ZDliLWRiNWQtNGY5My04YzI5LWY1ZDhhNWQ4OWNiOQpJTkZFUkVOQ0VfQ09OVEFJTkVSPW52Y3IuaW8vbXlvcmcvZ3B0LTMuNS10dXJiby1maW5lLXR1bmU6MS4wLjAKSU5GRVJFTkNFX0NPTlRBSU5FUl9BUkdTPS1hcmcxPXRlc3QxIGFyZzI9dGVzdDIKSU5GRVJFTkNFX0NPTlRBSU5FUl9FTlY9VzNzaWEyVjVJam9pU1U1R1JWSkZUa05GWDBWT1ZsOUxSVmtpTENKMllXeDFaU0k2SW1sdVptVnlaVzVqWlY5MllXeDFaU0o5WFE9PQpJTkZFUkVOQ0VfSEVBTFRIX0VORFBPSU5UPS92Mi9oZWFsdGgvcmVhZHkKSU5GRVJFTkNFX0hFQUxUSF9FWFBFQ1RFRF9SRVNQT05TRV9DT0RFPSIyMDAiCklORkVSRU5DRV9IRUFMVEhfUE9SVD0iNTAwNTEiCklORkVSRU5DRV9QT1JUPSI1MDA1MSIKSU5GRVJFTkNFX1BST1RPQ09MPUdSUEMKSU5GRVJFTkNFX1VSTD0vZ3JwYwpJTklUX0NPTlRBSU5FUj1udmNyLmlvL3F0ZnB0MWgwYmlldS9udmNmLWNvcmUvbnZjZl93b3JrZXJfaW5pdDowLjI0LjEwCk1BWF9SRVFVRVNUX0NPTkNVUlJFTkNZPSIxIgpOQ0FfSUQ9X2xJTFhCLTFOZk5tQm5RU2tfc3BxVldPdENBWFFtNTBVRU13ajNUUmd5bUpKMkF5dXdjZ3hxCk5WQ0ZfRlFETj1odHRwczovL3VzLXdlc3QtMi5hcGkubnZjZi5udmlkaWEuY29tCk5WQ0ZfRlFETl9HUlBDPWh0dHBzOi8vZ3JwYy5hcGkubnZjZi5udmlkaWEuY29tCk5WQ0ZfRlFETl9OQVRTPXRsczovL3VzLXdlc3QtMi5hd3MuY2xvdWQubmF0cy5udmNmLm52aWRpYS5jb206NDIyMgpOVkNGX1dPUktFUl9UT0tFTj10b2sKT1RFTF9DT05UQUlORVI9bnZjci5pby9xdGZwdDFoMGJpZXUvbnZjZi1jb3JlL29wZW50ZWxlbWV0cnktY29sbGVjdG9yOjAuNzQuMApPVEVMX0VYUE9SVEVSX09UTFBfRU5EUE9JTlQ9aHR0cHM6Ly9wcm9kLm90ZWwua2FpemVuLm52aWRpYS5jb206ODI4MgpUUkFDSU5HX0FDQ0VTU19UT0tFTj10cmFjZS10b2stMQpVVElMU19DT05UQUlORVI9bnZjci5pby9xdGZwdDFoMGJpZXUvbnZjZi1jb3JlL252Y2Zfd29ya2VyX3V0aWxzOjIuMjEuNApDT05UQUlORVJfUkVHSVNUUklFU19DUkVERU5USUFMUz1leUpyT0hOVFpXTnlaWFJ6SWpwYmV5SmhkWFJvY3lJNmV5SnVkbU55TG1sdklqcDdJbUYxZEdnaU9pSm1iMjlpWVhJaWZYMTlYWDBLClNJREVDQVJfUkVHSVNUUllfQ1JFREVOVElBTD1leUpoZFhSb2N5STZleUp1ZG1OeUxtbHZJanA3SW1GMWRHZ2lPaUptYjI5aVlYSWlmWDE5Cg==`,
				CacheLaunchSpecification: &common.CacheLaunchSpecification{
					CacheArtifacts: true,
					CacheHandle:    "abc123handle",
					CacheSize:      200000000,
				},
			},
		},
	}
	var err error
	req, err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Create(ctx, req, metav1.CreateOptions{})
	require.NoError(t, err)

	err = kb.ApplyCreationMessage(ctx, req)
	require.NoError(t, err)

	req, err = clients.BART.NvcaV2beta1().ICMSRequests(bc.requestsNamespace).Get(ctx, req.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotNil(t, req.Spec.CreationMsgInfo.FunctionLaunchSpecification)
	assert.Len(t, req.Spec.CreationMsgInfo.LaunchArtifacts, 5)

	err = kb.ApplyCreationMessage(ctx, req)
	require.EqualError(t, err, "model caching is still in progress")

	expInitJobName := "writer-job-abc123handle"
	initJob, err := clients.K8s.BatchV1().Jobs(bc.requestsNamespace).Get(ctx, expInitJobName, metav1.GetOptions{})
	require.NoError(t, err)
	initJob.Status.CompletionTime = &metav1.Time{Time: time.Now()}
	initJob.Status.Succeeded++
	_, err = clients.K8s.BatchV1().Jobs(bc.requestsNamespace).UpdateStatus(ctx, initJob, metav1.UpdateOptions{})
	require.NoError(t, err)

	rwPVCName := "rw-pvc-abc123handle"
	pvName := "pvc-abc123handle"
	pv := &v1.PersistentVolume{}
	pv.Name = pvName
	pv.Labels = map[string]string{
		fnVersionIDLabelString: cmsgLS.Details.FunctionVersionID,
	}
	pv.Spec = v1.PersistentVolumeSpec{
		ClaimRef: &v1.ObjectReference{
			Name:      rwPVCName,
			Namespace: bc.podInstanceNamespace,
		},
	}
	pv, err = clients.K8s.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
	require.NoError(t, err)
	pv.Status.Phase = v1.VolumeBound
	_, err = clients.K8s.CoreV1().PersistentVolumes().UpdateStatus(ctx, pv, metav1.UpdateOptions{})
	require.NoError(t, err)

	rwPVC, err := clients.K8s.CoreV1().PersistentVolumeClaims(bc.requestsNamespace).Get(ctx, rwPVCName, metav1.GetOptions{})
	require.NoError(t, err)
	rwPVC.Spec.VolumeName = pv.Name
	_, err = clients.K8s.CoreV1().PersistentVolumeClaims(bc.requestsNamespace).Update(ctx, rwPVC, metav1.UpdateOptions{})
	require.NoError(t, err)
	rwPVC.Status.Phase = v1.ClaimBound
	_, err = clients.K8s.CoreV1().PersistentVolumeClaims(bc.requestsNamespace).UpdateStatus(ctx, rwPVC, metav1.UpdateOptions{})
	require.NoError(t, err)

	err = kb.ApplyCreationMessage(ctx, req)
	require.EqualError(t, err, "model caching is still in progress")

	roPVCName := "ro-pvc-abc123handle"
	roPVC, err := clients.K8s.CoreV1().PersistentVolumeClaims(bc.requestsNamespace).Get(ctx, roPVCName, metav1.GetOptions{})
	require.NoError(t, err)
	roPVC.Spec.VolumeName = pv.Name
	_, err = clients.K8s.CoreV1().PersistentVolumeClaims(bc.requestsNamespace).Update(ctx, roPVC, metav1.UpdateOptions{})
	require.NoError(t, err)
	roPVC.Status.Phase = v1.ClaimBound
	_, err = clients.K8s.CoreV1().PersistentVolumeClaims(bc.requestsNamespace).UpdateStatus(ctx, roPVC, metav1.UpdateOptions{})
	require.NoError(t, err)

	_, err = clients.K8s.CoreV1().PersistentVolumeClaims(bc.requestsNamespace).Get(ctx, rwPVCName, metav1.GetOptions{})
	require.True(t, errors.IsNotFound(err))

	err = kb.ApplyCreationMessage(ctx, req)
	require.NoError(t, err)

	// Ensure image cred update job requeues before instance apply.
	podList, err := clients.K8s.CoreV1().Pods(bc.requestsNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, podList.Items, 0)

	gotJob, err := clients.K8s.BatchV1().Jobs(bc.systemNamespace).Get(ctx, req.Name+"-cred-init", metav1.GetOptions{})
	require.NoError(t, err)
	gotJob.Status = batchv1.JobStatus{
		Conditions: []batchv1.JobCondition{{
			Type:   batchv1.JobComplete,
			Status: corev1.ConditionTrue,
			Reason: "done",
		}},
	}
	_, err = clients.K8s.BatchV1().Jobs(bc.systemNamespace).UpdateStatus(ctx, gotJob, metav1.UpdateOptions{})
	require.NoError(t, err)

	err = kb.ApplyCreationMessage(ctx, req)
	require.NoError(t, err)

	podList, err = clients.K8s.CoreV1().Pods(bc.requestsNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, podList.Items, 2)
	sort.Slice(podList.Items, func(i, j int) bool {
		return podList.Items[i].Name < podList.Items[j].Name
	})
	assert.Equal(t, "0-sr-randomRequestID1", podList.Items[0].Name)
	assert.Equal(t, "1-sr-randomRequestID1", podList.Items[1].Name)
	for _, pod := range podList.Items {
		assert.Equal(t, v1.Volume{
			Name: "model-data",
			VolumeSource: v1.VolumeSource{
				PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
					ClaimName: roPVCName,
					ReadOnly:  true,
				},
			},
		}, pod.Spec.Volumes[0])
	}
}

func TestCleanupNamespace(t *testing.T) {
	ctx := newTestContext()
	nsClient := mockKubeClients().K8s.CoreV1().Namespaces()
	namespaceName := "test-namespace"
	defaultTimeConfig := (&k8sutil.TimeConfig{}).Complete()

	tests := []struct {
		name           string
		setupNamespace func() error
		wantDeleted    bool
		wantError      bool
	}{
		{
			name: "namespace does not exist",
			setupNamespace: func() error {
				return nil
			},
			wantDeleted: false,
			wantError:   false,
		},
		{
			name: "namespace exists and not terminating",
			setupNamespace: func() error {
				ns := &v1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: namespaceName,
					},
				}
				_, err := nsClient.Create(ctx, ns, metav1.CreateOptions{})
				return err
			},
			wantDeleted: true,
			wantError:   false,
		},
		{
			name: "namespace exists and is terminating",
			setupNamespace: func() error {
				now := metav1.Now()
				ns := &v1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name:              namespaceName,
						DeletionTimestamp: &now,
					},
				}
				_, err := nsClient.Create(ctx, ns, metav1.CreateOptions{})
				return err
			},
			wantDeleted: false,
			wantError:   false,
		},
		{
			name: "namespace exists and is stuck terminating",
			setupNamespace: func() error {
				past := metav1.NewTime(time.Now().Add(-defaultTimeConfig.NamespaceStuckTimeout - time.Hour))
				ns := &v1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name:              namespaceName,
						DeletionTimestamp: &past,
					},
					Status: v1.NamespaceStatus{
						Phase: v1.NamespaceTerminating,
						Conditions: []v1.NamespaceCondition{
							{
								Type:   v1.NamespaceDeletionDiscoveryFailure,
								Status: v1.ConditionTrue,
							},
						},
					},
				}
				_, err := nsClient.Create(ctx, ns, metav1.CreateOptions{})
				return err
			},
			wantDeleted: false,
			wantError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			if err := tt.setupNamespace(); err != nil {
				t.Fatalf("failed to setup test: %v", err)
			}

			// Create wrapper functions to match the new signature
			getNamespace := func(name string) (*v1.Namespace, error) {
				return nsClient.Get(ctx, name, metav1.GetOptions{})
			}
			deleteNamespace := func(ctx context.Context, name string, options metav1.DeleteOptions) error {
				return nsClient.Delete(ctx, name, options)
			}

			// Execute
			deleted, err := cleanupNamespace(ctx, getNamespace, deleteNamespace, namespaceName)

			// Verify
			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.wantDeleted, deleted)

			// Cleanup
			_ = nsClient.Delete(ctx, namespaceName, metav1.DeleteOptions{})
		})
	}
}

func TestGetICMSRequestUpdatesForEvictionMaintenanceTermination(t *testing.T) {
	ctx := newTestContext()
	c := K8sComputeBackend{}

	// Test regular termination request
	regularRequest := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "regular-termination",
			Namespace: "test-namespace",
			Labels: map[string]string{
				SQSMessageIDKey: "regular-msg-123",
			},
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID: "req-123",
			Action:    common.TerminationAction,
		},
		Status: nvcav2beta1.ICMSRequestStatus{
			Instances: map[string]nvcav2beta1.InstanceStatus{
				"instance-1": {
					ID:                 "instance-1",
					Status:             "running",
					LastReportedStatus: "running",
				},
			},
		},
	}

	// Test eviction maintenance termination request
	evictionRequest := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "eviction-termination",
			Namespace: "test-namespace",
			Labels: map[string]string{
				SQSMessageIDKey: "evict-maint-req-456-0707090405",
			},
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID: "req-456",
			Action:    common.TerminationAction,
		},
		Status: nvcav2beta1.ICMSRequestStatus{
			Instances: map[string]nvcav2beta1.InstanceStatus{
				"instance-2": {
					ID:                 "instance-2",
					Status:             "running",
					LastReportedStatus: "running",
				},
			},
		},
	}

	// Test regular termination request
	updates := c.GetICMSRequestUpdatesForTerminationRequest(ctx, regularRequest)
	assert.Len(t, updates, 1)
	assert.Equal(t, types.ICMSRequestInstanceTerminatedByUser, updates[0].Payload.Status)
	assert.Equal(t, types.ICMSInstanceTerminated, updates[0].Payload.InstanceState)
	assert.Equal(t, types.ICMSInstanceRequestClosed, updates[0].Payload.RequestState)
	assert.Equal(t, types.ICMSInstanceState(""), updates[0].Payload.TerminationCause)
	assert.Equal(t, "", updates[0].Payload.SystemFailure)

	// Test eviction maintenance termination request
	updates = c.GetICMSRequestUpdatesForTerminationRequest(ctx, evictionRequest)
	assert.Len(t, updates, 1)
	assert.Equal(t, types.ICMSRequestInstanceTerminatedByService, updates[0].Payload.Status)
	assert.Equal(t, types.ICMSInstanceTerminated, updates[0].Payload.InstanceState)
	assert.Equal(t, types.ICMSInstanceRequestClosed, updates[0].Payload.RequestState)
	assert.Equal(t, types.ICMSInstanceTerminatedServiceMaintenance, updates[0].Payload.TerminationCause)
	assert.Equal(t, string(types.ICMSInstanceTerminatedServiceMaintenance), updates[0].Payload.SystemFailure)

	// Test request with already terminated instance (should be skipped)
	terminatedRequest := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "terminated-request",
			Namespace: "test-namespace",
			Labels: map[string]string{
				SQSMessageIDKey: "evict-maint-req-789-0707090405",
			},
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID: "req-789",
			Action:    common.TerminationAction,
		},
		Status: nvcav2beta1.ICMSRequestStatus{
			Instances: map[string]nvcav2beta1.InstanceStatus{
				"instance-3": {
					ID:                 "instance-3",
					Status:             string(types.ICMSInstanceTerminated),
					LastReportedStatus: string(types.ICMSInstanceTerminated),
				},
			},
		},
	}

	updates = c.GetICMSRequestUpdatesForTerminationRequest(ctx, terminatedRequest)
	assert.Len(t, updates, 0)
}

func TestEvictionMaintenanceMessageIDDetection(t *testing.T) {
	ctx := newTestContext()
	c := K8sComputeBackend{}

	testCases := []struct {
		name                     string
		messageID                string
		expectedIsEvictionMaint  bool
		expectedTerminationCause types.ICMSInstanceState
		expectedSystemFailure    string
	}{
		{
			name:                     "Regular termination message",
			messageID:                "regular-msg-123",
			expectedIsEvictionMaint:  false,
			expectedTerminationCause: types.ICMSInstanceState(""),
			expectedSystemFailure:    "",
		},
		{
			name:                     "Eviction maintenance message",
			messageID:                "evict-maint-req-456-0707090405",
			expectedIsEvictionMaint:  true,
			expectedTerminationCause: types.ICMSInstanceTerminatedServiceMaintenance,
			expectedSystemFailure:    string(types.ICMSInstanceTerminatedServiceMaintenance),
		},
		{
			name:                     "Eviction maintenance message with different timestamp",
			messageID:                "evict-maint-req-789-0101123456",
			expectedIsEvictionMaint:  true,
			expectedTerminationCause: types.ICMSInstanceTerminatedServiceMaintenance,
			expectedSystemFailure:    string(types.ICMSInstanceTerminatedServiceMaintenance),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			request := &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-request",
					Namespace: "test-namespace",
					Labels: map[string]string{
						SQSMessageIDKey: tc.messageID,
					},
				},
				Spec: nvcav2beta1.ICMSRequestSpec{
					RequestID: "test-req-123",
					Action:    common.TerminationAction,
				},
				Status: nvcav2beta1.ICMSRequestStatus{
					Instances: map[string]nvcav2beta1.InstanceStatus{
						"instance-1": {
							ID:                 "instance-1",
							Status:             "running",
							LastReportedStatus: "running",
						},
					},
				},
			}

			updates := c.GetICMSRequestUpdatesForTerminationRequest(ctx, request)
			assert.Len(t, updates, 1)

			if tc.expectedIsEvictionMaint {
				assert.Equal(t, types.ICMSRequestInstanceTerminatedByService, updates[0].Payload.Status)
				assert.Equal(t, tc.expectedTerminationCause, updates[0].Payload.TerminationCause)
				assert.Equal(t, tc.expectedSystemFailure, updates[0].Payload.SystemFailure)
			} else {
				assert.Equal(t, types.ICMSRequestInstanceTerminatedByUser, updates[0].Payload.Status)
				assert.Equal(t, tc.expectedTerminationCause, updates[0].Payload.TerminationCause)
				assert.Equal(t, tc.expectedSystemFailure, updates[0].Payload.SystemFailure)
			}
		})
	}
}

func TestPurgeInstanceID(t *testing.T) {
	ctx := newTestContext()

	testCases := []struct {
		name             string
		instanceID       string
		instanceType     nvcav2beta1.InstanceType
		setupMocks       func(clients *kubeclients.KubeClients, kb K8sComputeBackend, req *nvcav2beta1.ICMSRequest)
		expectTerminated bool
		expectError      bool
	}{
		{
			name:         "Pod instance termination success",
			instanceID:   "test-pod-instance",
			instanceType: nvcav2beta1.InstanceTypePod,
			setupMocks: func(clients *kubeclients.KubeClients, kb K8sComputeBackend, req *nvcav2beta1.ICMSRequest) {
				// Create a pod that will be deleted
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-instance",
						Namespace: kb.bk8s.podInstanceNamespace,
					},
				}
				_, err := clients.K8s.CoreV1().Pods(kb.bk8s.podInstanceNamespace).Create(ctx, pod, metav1.CreateOptions{})
				require.NoError(t, err)
			},
			expectTerminated: true,
		},
		{
			name:         "Pod instance already deleted",
			instanceID:   "nonexistent-pod",
			instanceType: nvcav2beta1.InstanceTypePod,
			setupMocks: func(clients *kubeclients.KubeClients, kb K8sComputeBackend, req *nvcav2beta1.ICMSRequest) {
				// No setup needed - pod doesn't exist
			},
			expectTerminated: true, // Should still report as terminated when not found
		},
		{
			name:         "MiniService instance termination",
			instanceID:   "miniservice-test-instance-miniservice",
			instanceType: nvcav2beta1.InstanceTypeMiniService,
			setupMocks: func(clients *kubeclients.KubeClients, kb K8sComputeBackend, req *nvcav2beta1.ICMSRequest) {
				// For miniservice, we need to mock the HelmV2 client
				// Since we're using fake clients, the Get operation will return NotFound error
				// which is handled in the purgeInstanceID method
			},
			expectTerminated: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup test environment
			clients := mockKubeClients()

			b, _, err := NewBackendk8sCacheBuilder().
				WithNamespaceLabels(labels.Set{"bartnamespace": "true"}).
				WithClients(clients).
				Start(ctx)
			require.NoError(t, err)

			kbInterface, _ := NewK8sComputeBackend(clients, b)
			kb := kbInterface.(K8sComputeBackend)

			// Create a test ICMSRequest
			req := &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-request",
					Namespace: "default",
				},
				Spec: nvcav2beta1.ICMSRequestSpec{
					RequestID: "test-request-id",
				},
			}

			// Setup mocks for the test case
			tc.setupMocks(clients, kb, req)

			// Initialize terminated instances map
			terminatedInstances := make(map[string]nvcav2beta1.InstanceStatus)

			// Call PurgeInstanceID
			result := kb.PurgeInstanceID(ctx, req, terminatedInstances, tc.instanceID)

			// Verify results
			assert.Equal(t, tc.expectTerminated, result, "Expected termination result mismatch")

			if tc.expectTerminated {
				// Check that the instance was added to terminatedInstances map
				instanceStatus, exists := terminatedInstances[tc.instanceID]
				assert.True(t, exists, "Instance should be in terminatedInstances map")
				assert.Equal(t, tc.instanceID, instanceStatus.ID)
				assert.Equal(t, tc.instanceType, instanceStatus.Type)
				assert.Equal(t, string(types.ICMSInstanceTerminated), instanceStatus.Status)
				assert.Equal(t, string(types.ICMSInstanceStateNoStatus), instanceStatus.LastReportedStatus)
			}
		})
	}
}

func TestPurgeInstanceID_ErrorHandling(t *testing.T) {
	ctx := newTestContext()

	// Test error scenario where namespace deletion fails
	clients := mockKubeClients()

	b, _, err := NewBackendk8sCacheBuilder().
		WithNamespaceLabels(labels.Set{"bartnamespace": "true"}).
		WithClients(clients).
		Start(ctx)
	require.NoError(t, err)

	kbInterface, _ := NewK8sComputeBackend(clients, b)
	kb := kbInterface.(K8sComputeBackend)

	req := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-request",
			Namespace: "default",
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID: "test-request-id",
		},
	}

	terminatedInstances := make(map[string]nvcav2beta1.InstanceStatus)

	// Test with an instance that's already in the terminatedInstances map
	instanceID := "already-terminated-instance"
	terminatedInstances[instanceID] = nvcav2beta1.InstanceStatus{
		ID:                    instanceID,
		Type:                  nvcav2beta1.InstanceTypePod,
		Status:                string(types.ICMSInstanceTerminated),
		LastReportedStatus:    string(types.ICMSInstanceStateNoStatus),
		LastReportedTimestamp: nil,
	}

	result := kb.PurgeInstanceID(ctx, req, terminatedInstances, instanceID)

	// Should return false since instance was already terminated
	assert.False(t, result, "Should return false for already terminated instance")
}

func TestGetICMSRequestUpdatesForCreatePodRequest_CreateContainerError_EventLogInHealthInfo(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClients()
	ns := RequestsNamespace

	bc := &BackendK8sCache{
		podInstanceNamespace: ns,
		systemNamespace:      SystemNamespace,
		featureFlagFetcher:   &featureflagmock.Fetcher{},
		clients:              clients,
		k8sTimeConfig:        (&k8sutil.TimeConfig{}).Complete(),
	}
	kb := K8sComputeBackend{
		clients: clients,
		bk8s:    bc,
	}

	podName := "create-err-pod"
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
		},
		Status: v1.PodStatus{
			Phase: v1.PodPending,
			Conditions: []v1.PodCondition{
				{Type: v1.PodScheduled, Status: v1.ConditionTrue},
			},
			StartTime: &metav1.Time{Time: time.Now().Add(-5 * time.Minute)},
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name: "main",
					State: v1.ContainerState{
						Waiting: &v1.ContainerStateWaiting{
							Reason:  "CreateContainerError",
							Message: "container status message",
						},
					},
				},
			},
		},
	}
	_, err := clients.K8s.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	require.NoError(t, err)

	eventMessage := "failed to create containerd task: rpc error: code = Unknown desc = failed to create shim task"
	k8sEvent := &v1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName + ".0",
			Namespace: ns,
		},
		InvolvedObject: v1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  ns,
			Name:       podName,
		},
		Reason:  "CreateContainerError",
		Message: eventMessage,
	}
	_, err = clients.K8s.CoreV1().Events(ns).Create(ctx, k8sEvent, metav1.CreateOptions{})
	require.NoError(t, err)

	req := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "sr-1", Namespace: ns},
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID: "req-1",
			Action:    common.FunctionCreationAction,
		},
		Status: nvcav2beta1.ICMSRequestStatus{
			Instances: map[string]nvcav2beta1.InstanceStatus{
				podName: {
					ID:                 podName,
					Type:               nvcav2beta1.InstanceTypePod,
					Status:             string(types.ICMSInstanceStarted),
					LastReportedStatus: string(types.ICMSInstanceStarted),
				},
			},
		},
	}
	st := req.Status.Instances[podName]

	update, err := kb.GetICMSRequestUpdatesForCreatePodRequest(ctx, st, req)
	require.NoError(t, err)
	require.NotEqual(t, types.ICMSRequestUpdateInfo{}, update)
	assert.Equal(t, types.ICMSInstanceTerminated, update.Payload.InstanceState)
	assert.Equal(t, types.ICMSInstanceFailedCreateContainerError, update.Payload.TerminationCause)
	assert.Contains(t, update.Payload.HealthInfo.ErrorLog, "Kubernetes events:")
	assert.Contains(t, update.Payload.HealthInfo.ErrorLog, "[CreateContainerError]")
	assert.Contains(t, update.Payload.HealthInfo.ErrorLog, eventMessage)
}

func TestImageCredentialUpdater(t *testing.T) {
	ctx := newTestContext()

	clients := makeMockKubeClients()
	bc := &BackendK8sCache{
		podInstanceNamespace: RequestsNamespace,
		systemNamespace:      SystemNamespace,
		featureFlagFetcher:   &featureflagmock.Fetcher{},
		clients:              clients,
		eventRecorder:        record.NewFakeRecorder(10),
		k8sTimeConfig:        (&k8sutil.TimeConfig{}).Complete(),
	}
	kb := K8sComputeBackend{
		scheme:  runtime.NewScheme(),
		clients: clients,
		bk8s:    bc,
	}
	configuredToleration := corev1.Toleration{
		Key:      "nvidia.com/test-workload",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}
	bc.cfg.Workload.Tolerations = []corev1.Toleration{configuredToleration}

	sr := &nvcav2beta1.ICMSRequest{}
	sr.UID = "0e55f7ae-8c40-4397-a510-d4a655cf734e"
	sr.Name, sr.Namespace = "sr-1b9ed9a3-a583-49f4-b729-caa58a94a3d6", RequestsNamespace
	sr.Spec.RequestID = "reqid1"
	sr.Spec.MessageBatchID = "msgbatchid1"
	sr.Spec.CreationMsgInfo.FunctionLaunchSpecification = &function.LaunchSpecification{
		EnvironmentB64: base64.StdEncoding.EncodeToString(fmt.Appendf(nil,
			"%s=%s\n%s=%s",
			common.ContainerRegistriesCredentialsEnv, "foobar",
			common.SidecarRegistryCredentialEnv, "foobar",
		)),
	}

	labelsForReq := nvcatypes.GetLabelsForRequest(sr, kb.bk8s.featureFlagFetcher)
	annosForReq := nvcatypes.GetAnnotationsForRequest(sr)
	ownerRefsForReq := getOwnerRefForRequest(sr)

	mf := func(obj client.Object) {
		obj.SetLabels(mergeMaps(obj.GetLabels(), labelsForReq))
		obj.SetAnnotations(mergeMaps(obj.GetAnnotations(), annosForReq))
		obj.SetOwnerReferences(ownerRefsForReq)
	}

	// No image set.
	err := bc.ensureImageCredentialUpdaterCronJob(ctx)
	require.NoError(t, err)

	requeue, err := kb.initializeImageCredentialHelper(ctx, sr, mf)
	require.NoError(t, err)
	assert.False(t, requeue)

	// Image set, objects should be created.
	bc.imageCredentialHelperImage = "image-credential-helper:latest"

	err = bc.ensureImageCredentialUpdaterCronJob(ctx)
	require.NoError(t, err)

	requeue, err = kb.initializeImageCredentialHelper(ctx, sr, mf)
	require.NoError(t, err)
	assert.True(t, requeue)

	gotSecret, err := clients.K8s.CoreV1().Secrets(bc.podInstanceNamespace).Get(ctx, sr.Name+"-image-creds", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "foobar", string(gotSecret.Data[common.ContainerRegistriesCredentialsEnv]))
	assert.Equal(t, "foobar", string(gotSecret.Data[common.SidecarRegistryCredentialEnv]))

	gotJob, err := clients.K8s.BatchV1().Jobs(bc.systemNamespace).Get(ctx, sr.Name+"-cred-init", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"/usr/bin/image-credential-helper"},
		gotJob.Spec.Template.Spec.Containers[0].Command,
	)
	assert.Equal(t,
		[]string{"-global", "-target-namespace", "nvcf-backend", "-secret-label-selector", "icms-request-id=reqid1,nvcf.nvidia.io/message-batch-id=msgbatchid1"},
		gotJob.Spec.Template.Spec.Containers[0].Args,
	)
	assert.Empty(t, gotJob.Spec.Template.Spec.ImagePullSecrets)
	assert.Equal(t, "nvca", gotJob.Spec.Template.Spec.ServiceAccountName)
	assert.Contains(t, gotJob.Spec.Template.Spec.Tolerations, configuredToleration)

	gotCronJob, err := clients.K8s.BatchV1().CronJobs(bc.systemNamespace).Get(ctx, "image-cred-updater", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"/usr/bin/image-credential-helper"},
		gotCronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Command,
	)
	assert.Equal(t,
		[]string{"-global", "-namespace-label-selector", "nvca.nvcf.nvidia.io/workload-instance-type"},
		gotCronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Args,
	)
	assert.Equal(t, "nvca", gotCronJob.Spec.JobTemplate.Spec.Template.Spec.ServiceAccountName)
	assert.Empty(t, gotCronJob.Spec.JobTemplate.Spec.Template.Spec.ImagePullSecrets)

	// Update job to failed, should see error.
	gotJob.Status = batchv1.JobStatus{
		Conditions: []batchv1.JobCondition{{
			Type:   batchv1.JobFailed,
			Status: corev1.ConditionTrue,
			Reason: "reason",
		}},
	}
	_, err = clients.K8s.BatchV1().Jobs(bc.systemNamespace).UpdateStatus(ctx, gotJob, metav1.UpdateOptions{})
	require.NoError(t, err)
	requeue, err = kb.initializeImageCredentialHelper(ctx, sr, mf)
	require.EqualError(t, err, "terminal error: image pull updater init job failed: reason")
	assert.False(t, requeue)

	// Update job to completed, should see no requeue.
	gotJob.Status = batchv1.JobStatus{
		Conditions: []batchv1.JobCondition{{
			Type:   batchv1.JobComplete,
			Status: corev1.ConditionTrue,
			Reason: "done",
		}},
	}
	_, err = clients.K8s.BatchV1().Jobs(bc.systemNamespace).UpdateStatus(ctx, gotJob, metav1.UpdateOptions{})
	require.NoError(t, err)
	requeue, err = kb.initializeImageCredentialHelper(ctx, sr, mf)
	require.NoError(t, err)
	assert.False(t, requeue)

	for _, obj := range []metav1.Object{gotSecret} {
		assert.Equal(t, ownerRefsForReq, obj.GetOwnerReferences())
	}
	for _, obj := range []metav1.Object{gotCronJob, gotJob} {
		assert.Empty(t, obj.GetOwnerReferences())
	}
}

func TestApplyCustomAnnotationsIfGPU(t *testing.T) {
	tests := []struct {
		name                string
		pod                 *v1.Pod
		customAnnotations   *sync.Map
		expectAnnotations   map[string]string
		expectNoAnnotations bool
	}{
		{
			name: "Pod with nvidia.com/gpu requests gets custom annotations",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "container1",
							Image: "test-image",
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									"nvidia.com/gpu": resource.MustParse("1"),
								},
							},
						},
					},
				},
			},
			customAnnotations: func() *sync.Map {
				sm := &sync.Map{}
				sm.Store("annotations", map[string]string{
					"custom-annotation-1": "value-1",
					"custom-annotation-2": "value-2",
				})
				return sm
			}(),
			expectAnnotations: map[string]string{
				"custom-annotation-1": "value-1",
				"custom-annotation-2": "value-2",
			},
		},
		{
			name: "Pod with nvidia.com/pgpu limits gets custom annotations",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "container1",
							Image: "test-image",
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									"nvidia.com/pgpu": resource.MustParse("2"),
								},
							},
						},
					},
				},
			},
			customAnnotations: func() *sync.Map {
				sm := &sync.Map{}
				sm.Store("annotations", map[string]string{
					"custom-key": "custom-value",
				})
				return sm
			}(),
			expectAnnotations: map[string]string{
				"custom-key": "custom-value",
			},
		},
		{
			name: "Pod with nvidia.com/gpu.shared in init container gets custom annotations",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: v1.PodSpec{
					InitContainers: []v1.Container{
						{
							Name:  "init-container",
							Image: "test-image",
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									"nvidia.com/gpu.shared": resource.MustParse("1"),
								},
							},
						},
					},
					Containers: []v1.Container{
						{
							Name:  "container1",
							Image: "test-image",
						},
					},
				},
			},
			customAnnotations: func() *sync.Map {
				sm := &sync.Map{}
				sm.Store("annotations", map[string]string{
					"annotation": "value",
				})
				return sm
			}(),
			expectAnnotations: map[string]string{
				"annotation": "value",
			},
		},
		{
			name: "Pod with existing annotations gets merged with custom annotations",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
					Annotations: map[string]string{
						"existing-annotation": "existing-value",
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "container1",
							Image: "test-image",
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									"nvidia.com/gpu": resource.MustParse("1"),
								},
							},
						},
					},
				},
			},
			customAnnotations: func() *sync.Map {
				sm := &sync.Map{}
				sm.Store("annotations", map[string]string{
					"custom-annotation": "custom-value",
				})
				return sm
			}(),
			expectAnnotations: map[string]string{
				"existing-annotation": "existing-value",
				"custom-annotation":   "custom-value",
			},
		},
		{
			name: "Pod with GPU but nil custom annotations cache",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "container1",
							Image: "test-image",
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									"nvidia.com/gpu": resource.MustParse("1"),
								},
							},
						},
					},
				},
			},
			customAnnotations:   nil,
			expectNoAnnotations: true,
		},
		{
			name: "Pod with GPU but empty custom annotations cache",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "container1",
							Image: "test-image",
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									"nvidia.com/gpu": resource.MustParse("1"),
								},
							},
						},
					},
				},
			},
			customAnnotations: func() *sync.Map {
				sm := &sync.Map{}
				sm.Store("annotations", map[string]string{})
				return sm
			}(),
			expectNoAnnotations: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Call k8sutil.ApplyCustomAnnotations
			k8sutil.ApplyCustomAnnotations(tt.pod, tt.customAnnotations)

			if tt.expectNoAnnotations {
				// Pod should have no annotations or only existing annotations
				if tt.pod.Annotations != nil {
					for key := range tt.expectAnnotations {
						assert.NotContains(t, tt.pod.Annotations, key, "Pod should not have custom annotation %s", key)
					}
				}
			} else {
				// Verify all expected annotations are present
				require.NotNil(t, tt.pod.Annotations, "Pod annotations should not be nil")
				for key, expectedValue := range tt.expectAnnotations {
					assert.Equal(t, expectedValue, tt.pod.Annotations[key], "Annotation %s mismatch", key)
				}
			}
		})
	}
}

func TestIsStartupArgsMalformed(t *testing.T) {
	tests := []struct {
		name     string
		errStr   string
		expected bool
	}{
		{
			name:     "empty string",
			errStr:   "",
			expected: false,
		},
		{
			name:     "unrelated error",
			errStr:   "connection refused",
			expected: false,
		},
		{
			name:     "shlex error via nvcf-icms-translate wrapping",
			errStr:   simulateMalformedArgsError(t),
			expected: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isStartupArgsMalformed(tt.errStr))
		})
	}
}

// simulateMalformedArgsError calls function.Translate from nvcf-icms-translate with
// a malformed INFERENCE_CONTAINER_ARGS value (unclosed quote) to produce the exact
// error string NVCA will see at runtime. If nvcf-icms-translate ever changes how it
// wraps or formats the shlex error, this test will break, alerting us to update
// isStartupArgsMalformed accordingly.
func simulateMalformedArgsError(t *testing.T) string {
	t.Helper()

	// Build a minimal base64-encoded environment with an unclosed-quote arg.
	// Registry credential envs (nvcf-icms-translate) must be set so pull-secret
	// parsing succeeds before we reach the shlex.Split call path.
	const (
		testContainerRegistriesCreds = "eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJudmNyLmlvIjp7ImF1dGgiOiJjM1JuTFdGaVl6RXlNem8yWmpZek5HUTROUzA1TXpGbExUUXhaall0WVRKbFl5MDJOMkk0TnpVd1pqRXlOMlU9In19fV19"
		testSidecarRegistryCred      = "eyJhdXRocyI6eyJudmNyLmlvIjp7ImF1dGgiOiJKRzloZFhSb2RHOXJaVzQ2Ym5aaGNHa3RjM1JuTFdGaVl6RXlNdz09In19fQo="
	)
	env := fmt.Sprintf("%s=nvcr.io/nvidia/fake-image:latest\n%s=nvcr.io/nvidia/fake-utils:latest\n%s=%s\n%s=%s\n%s=%s",
		common.ContainerFunctionImageEnv,
		common.UtilsImageEnv,
		common.ContainerRegistriesCredentialsEnv,
		testContainerRegistriesCreds,
		common.SidecarRegistryCredentialEnv,
		testSidecarRegistryCred,
		common.InferenceContainerArgsEnv,
		`arg1 "unclosed`,
	)
	envB64 := base64.StdEncoding.EncodeToString([]byte(env))

	msg := function.CreationQueueMessage{
		LaunchSpecification: &function.LaunchSpecification{
			EnvironmentB64: envB64,
		},
	}
	cfg := function.TranslateConfig{
		TranslateConfig: common.TranslateConfig{
			InstanceTypeLabelSelectorKey: "nvidia.com/gpu.product",
			WorkloadResources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					"nvidia.com/gpu": resource.MustParse("1"),
				},
			},
		},
	}

	_, err := function.Translate(msg, cfg)
	require.Error(t, err, "function.Translate should fail on malformed container args")
	return err.Error()
}
